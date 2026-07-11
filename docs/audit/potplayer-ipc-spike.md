# PotPlayer WM_USER IPC Spike

Date: 2026-07-11

Scope: this is a Windows-only diagnostic spike. It does not modify Navi's
database, `PlayFile`, player launch behavior, or background task lifecycle.

## Result

The installed PotPlayer accepts the candidate `WM_USER` request form for a
visible `PotPlayer64` top-level window. On this machine and version, the
following reads and the tested seek write work:

```text
SendMessageTimeoutW(HWND, 0x0400, opcode, lParam, ..., 250ms, ...)
```

This confirms a version-specific IPC capability, not a stable public contract.
The required elevated-PotPlayer / unelevated-probe case was not executed, so
this spike does not recommend changing the production playback flow yet.

## Local Installation

| Item | Observed value |
| --- | --- |
| Product | PotPlayer 64 bit |
| Registry display version | `26.07.01.0` |
| Executable | `C:\Program Files\DAUM\PotPlayer\PotPlayerMini64.exe` |
| EXE `FileVersion` / `ProductVersion` | `0,0,0,0`; not useful as a version source |
| Requested `CmdLine.txt` | Absent from the installation directory |
| Local command documentation actually present | `C:\Program Files\DAUM\PotPlayer\CmdLine64.txt` |

## Probe Delivered

`cmd/potplayer-probe` is Windows-only and has no production-package imports.

- It enumerates only `PotPlayer64` and `PotPlayer` through `EnumWindows`.
- Each candidate log row includes class, HWND, PID, process image path, and
  window title.
- Every operation that invokes the target window procedure uses
  `SendMessageTimeoutW` with `SMTO_BLOCK | SMTO_ABORTIFHUNG |
  SMTO_ERRORONEXIT` and a 250 ms timeout. `WM_GETTEXT` also uses this wrapper.
  Window enumeration and process-image queries do not dispatch to PotPlayer's
  window procedure.
- It samples the three candidate reads once per second after an explicit
  window selection.
- A seek needs `seek`, then an exact `YES`; it reads the position first, adds
  at most 10,000 candidate time units, sends one set operation, and reads back
  after 500 ms.
- It never opens a database or launches a background task.

Focused tests pass:

```powershell
go test ./cmd/potplayer-probe
```

The tests cover exact class filtering, selection validation, signed status
conversion, and bounded seek-target calculation. They intentionally do not
mock a PotPlayer protocol claim.

## Message Verification

The values below were not treated as confirmed until their output changed with
the corresponding local player state.

| Candidate | Request form | Local result |
| --- | --- | --- |
| `WM_USER` | `0x0400` message ID | Confirmed as the message ID used for all rows below. |
| `POT_GET_TOTAL_TIME` | `wParam=0x5002`, `lParam=0` | Returned `1674769` while the test file was open and `0` after the file was stopped. |
| `POT_GET_CURRENT_TIME` | `wParam=0x5004`, `lParam=0` | Increased by about 1,000 once per second during playback; remained constant while paused. |
| `POT_SET_CURRENT_TIME` | `wParam=0x5005`, `lParam=target` | Confirmed by readback: `191490 -> 201490` after the probe's explicit `seek` then `YES` flow. The message result was `0`, but the `SendMessageTimeoutW` call succeeded and the readback changed exactly. |
| `POT_GET_PLAY_STATUS` | `wParam=0x5006`, `lParam=0` | Returned `2` playing, `1` paused, and `-1` with no file/stopped. |

All successful target windows in this spike had class `PotPlayer64`; no live
window of class `PotPlayer` was observed. The probe retains both class names
because the requested enumeration must support either.

### Time Unit

The time values are milliseconds for this local build:

- Playing samples changed `119718 -> 120715 -> 121726 -> 122720` at roughly
  one-second intervals.
- The total `1674769` equals approximately `00:27:54.769`, matching the
  player-visible duration.
- Paused samples repeated `191490` across four one-second samples.

### State Code Evidence

| Player state | `current_time_ms` / `total_time_ms` | `play_status` |
| --- | --- | --- |
| Playing | Increasing / `1674769` | `2` |
| Paused | Stable / `1674769` | `1` |
| File closed or stopped, player still open | `0` / `0` | `-1` |

Forward and backward tests while paused moved the observed position
`289035 -> 294035 -> 289035`; the player remained at status `1`.

## Command-Line Evidence

The local `CmdLine64.txt` documents the following semantics. This is local
installation evidence, not an Internet-derived assumption.

| Argument | Local documentation | Observed behavior |
| --- | --- | --- |
| `/new` | Play specified content in a new program instance; the F5 > General > Multiple instances setting does not affect it. | With one visible playing instance, `/new <file>` produced a persistent second PID and a second `PotPlayer64` window. |
| `/current` | Play specified content in an existing instance; the same Multiple instances setting does not affect it. | With one visible blank instance, `/current <file>` started a short-lived request process and reused the existing window/PID. No additional persistent process appeared. |
| `/seek=time` | Start specified or last-played content at a time. Format is `hh:mm:ss.ms` or seconds, for example `/seek=1800`. | `/current /seek=30 <file>` reused the visible instance. The later observed position was consistent with playback continuing from a seeked point, but this spike did not synchronize a zero-time sample tightly enough to claim an exact 30,000 ms start boundary. |
| Dedicated single-instance switch | No explicit English CLI switch was found. | Not available from the local command documentation. |

An important edge case was observed before a visible target existed: the
pre-existing PID `53048` had `MainWindowHandle=0` and no matching top-level
window. In that state, an early `/current <file>` attempt created a visible
process instead of routing to that hidden process. Command-line routing cannot
be used as a reliable process-to-window binding mechanism.

## HWND, PID, and Multiple Instances

The two-instance run produced the following independent targets for the same
media title and executable path:

```text
class=PotPlayer64 HWND=0x21854  PID=47748
class=PotPlayer64 HWND=0x13199E PID=3524
```

They reported different current positions. Therefore:

- Treat the HWND as the actual IPC target identity.
- Retain PID as metadata and to obtain the image path, not as a global player
  identity.
- Do not assume a newly launched PotPlayer process is the window receiving
  playback; `/current` can exit after routing to an existing instance.
- A production design must explicitly decide which visible window is eligible
  for a media item and must handle multiple candidates without guessing.

## Scenario Matrix

| Requested scenario | Result | Evidence / limit |
| --- | --- | --- |
| PotPlayer not started | Partially covered | The probe cleanly returned no candidates. A true no-process run was not forced because pre-existing PID `53048` was preserved. |
| Started, no file | Passed | `PotPlayer64`, `HWND=0x451B44`, `PID=36072`, title `PotPlayer`, returned `0 / 0 / -1`. |
| One window playing a file | Passed | One selected `PotPlayer64` window returned increasing time, fixed total, status `2`. |
| Pause | Passed | Four samples retained `191490`, with status `1`. |
| Forward and backward | Passed | Observed `+5000` then `-5000` while paused. |
| Close file without closing PotPlayer | Passed | Same player window title returned to `PotPlayer`; values became `0 / 0 / -1`. |
| Close PotPlayer | Passed | A monitoring probe observed the close race, printed bounded timeout/invalid-handle results, then `Selected HWND=0x13199E no longer exists` and exited. |
| Two instances | Passed | Two visible `PotPlayer64` HWND/PID pairs existed simultaneously. |
| Existing single instance, open again from CLI | Passed with visibility caveat | `/current` reused a visible blank instance. It did not route to the pre-existing hidden no-HWND process. |
| Elevated PotPlayer, unelevated probe | Not executed | Both observed probe and test player tokens were non-elevated. No UAC elevation was requested or accepted. |

## Permissions

No integrity mismatch was available to test: the probe and selected PotPlayer
instances were non-elevated. The probe classifies `ERROR_ACCESS_DENIED` as
`access_denied`, rather than treating it as unsupported IPC, because Windows
UIPI can block an unelevated sender from messaging an elevated target. Formal
implementation needs a defined same-integrity requirement or a documented
degraded path before this can be considered complete.

## Recommendation

Do not modify the production playback flow yet.

The message protocol and millisecond/status interpretation are validated for
PotPlayer `26.07.01.0` and a visible `PotPlayer64` window, including a
read-back-verified seek. However, production work still needs a deliberate
window-binding policy for multiple instances, lifecycle behavior for a missing
or hidden target window, and an elevated-process policy. It also must reconcile
the current `PlayFile` behavior, which immediately marks media watched, with
any future real playback-progress semantics.
