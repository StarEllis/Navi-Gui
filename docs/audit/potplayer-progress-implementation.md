# Navi to PotPlayer Progress Sync Implementation

Date: 2026-07-11

Protocol source: `docs/audit/potplayer-ipc-spike.md`. No undocumented
PotPlayer message or status value is used.

## Implementation Boundary

This first version supports Windows PotPlayer only and only sessions launched
by Navi. A tracked launch uses the configured PotPlayer executable with
`/new <file>`, then binds the session to the single PotPlayer HWND owned by the
launched PID. That HWND never changes during polling. If a unique HWND cannot
be bound, playback remains running and progress sync is disabled with a logged
warning.

The implementation does not enumerate and adopt an arbitrary existing
PotPlayer session. It does not track files opened directly by the user. A
configured executable whose filename is not a recognized PotPlayer executable
is launched through the existing untracked flow. mpv, VLC, and the operating
system default player are not adapted.

The production protocol is limited to the locally verified values:

| Operation | Verified request |
| --- | --- |
| Current position | `WM_USER (0x0400)`, `wParam=0x5004` |
| Total duration | `WM_USER (0x0400)`, `wParam=0x5002` |
| Seek | `WM_USER (0x0400)`, `wParam=0x5005`, milliseconds in `lParam` |
| Playback status | `WM_USER (0x0400)`, `wParam=0x5006` |

Status values are `2=playing`, `1=paused`, and `-1=stopped/no file`. Every
message sent to a PotPlayer window procedure, including `WM_GETTEXT`, uses
`SendMessageTimeoutW` with `SMTO_BLOCK | SMTO_ABORTIFHUNG |
SMTO_ERRORONEXIT` and a 250 ms timeout.

`service/player` contains the protocol-independent `PlayerAdapter`, bound
`PlayerSession`, and `PlaybackSessionManager`. Windows contains the Win32
adapter; non-Windows builds return an explicit unsupported error. Tests use a
fake adapter and never require PotPlayer.

## Session And Persistence Semantics

- Startup creates or restores `desktop_user` idempotently.
- History is loaded and saved by `mediaID`; playback performs no exact
  `file_path` reverse lookup.
- Watch history writes use a transaction and conflict Upsert on
  `(user_id, media_id)`.
- Launching a player does not set `completed` or `is_watched`. The first valid
  playback sample marks the media watched only after its database commit.
- Polling occurs every 3 seconds. Successful dirty state is normally written
  at most once per 15 seconds.
- Pause transition, a position jump of at least 30 seconds, stop, window
  closure, file identity change, and Navi shutdown request an immediate final
  write.
- Three consecutive IPC failures stop synchronization without terminating
  PotPlayer. Database writes retry three times with bounded waits; the latest
  in-memory observation stays dirty after failure and the final error is
  logged.
- The title is read before and after the position/duration/status sample. A
  clear mismatch stops the session before that sample can be written to the
  old `mediaID`.
- Replacing a session for the same media waits for the old session to finalize
  before the new session is registered. Different media sessions remain
  independent.

Watched state is not tied to a completion percentage. The first valid playback
sample committed for a Navi session sets `completed` and `is_watched`; a player
launch that never yields a valid sample does not.

Resume is attempted only after PotPlayer reports the expected loaded file.
Watched state does not disable resume. A saved position at or above 90% starts
from the beginning; seek failure is logged and normal playback continues.

## Frontend Updates

After a database commit, `media:state-updated` includes `media_id`, `position`,
`duration`, `progress_percent`, `completed`, `is_watched`, `last_watched_at`,
`playback_state`, and a process-monotonic `revision`.

The frontend rejects duplicate or older revisions and patches only the matching
media object in the visible grid, selected detail, detail cache, recommendation
cards, and persisted list caches. Cards and detail show progress without a
three-second full-list refresh. Watched/unwatched membership changes and active
`last_watched` sort-key changes invalidate the relevant visible pages; ordinary
position changes do not.

## Locally Verified PotPlayer

| Item | Value |
| --- | --- |
| Product | PotPlayer 64 bit |
| Registry display version | `26.07.01.0` |
| Executable | `C:\Program Files\DAUM\PotPlayer\PotPlayerMini64.exe` |
| Observed window class | `PotPlayer64` |

This version is the only protocol evidence for the implementation. PotPlayer's
WM_USER interface is not treated as a stable public contract.

## Unsupported Scenarios

- Non-Windows operating systems.
- mpv, VLC, the default player, or an executable not recognized as PotPlayer.
- Files opened outside Navi and arbitrary existing PotPlayer windows.
- Automatic adoption when `/new` does not yield one unique HWND owned by its
  launched PID.
- Continued tracking after the bound window displays a clearly different file.
- Cross-integrity IPC. An access-denied result instructs the user to run Navi
  and PotPlayer at the same privilege level, then degrades to launch-only.
- Guarantees for PotPlayer versions other than `26.07.01.0`.

## Automated Test Results

Executed on 2026-07-11:

| Command | Result |
| --- | --- |
| `go test -count=1 ./...` | Passed |
| `go vet ./...` | Passed |
| `cd frontend; npm test` | Passed, 28 tests |
| `cd frontend; npm run build` | Passed |
| Windows `amd64` compile of root and `service/player` tests | Passed |

Fake-player coverage includes normal playback, first-sample watched state,
pause, large seek, resume, near-end restart, window close, IPC timeout,
shutdown, file switch, two sessions, same-media replacement, and monotonic
event revisions. Frontend tests cover zero-valued progress, stale revision
rejection, and selective pagination invalidation.

The real-player integration test is isolated behind the
`windows && potplayer_integration` build constraint. It requires
`NAVI_POTPLAYER_EXE` and `NAVI_POTPLAYER_TEST_MEDIA`; ordinary `go test ./...`
does not open PotPlayer.

## Manual Acceptance Matrix

| Scenario | Action | Expected result | Status |
| --- | --- | --- | --- |
| Fresh play | Start an unwatched item from each of MediaCard, MediaDetail, RecommendationCard, and random play | PotPlayer opens a dedicated instance; launch alone does not mark watched; the first committed valid sample does | Pending manual run |
| Pause | Pause inside PotPlayer | Position is saved immediately and UI state becomes paused | Pending manual run |
| Seek | Jump forward and backward by more than 30 seconds | New position is saved immediately; old position is not restored | Pending manual run |
| Resume | Close at 10-80%, then play again from Navi | Seek occurs only after the expected file loads | Pending manual run |
| Near end | Close at or above 90%, then play again | Watched state remains true and playback starts from the beginning instead of resuming near the credits | Pending manual run |
| File switch | Open another file inside the bound PotPlayer instance | Synchronization stops; new file position is never written to the old media ID | Pending manual run |
| Window close | Close the bound PotPlayer window | Latest valid observation is saved and the session exits | Pending manual run |
| Hung player | Suspend or hang the bound player | Each call times out; sync stops after bounded failures; player is not killed | Pending manual run |
| Privilege mismatch | Run PotPlayer elevated and Navi normally | Clear same-privilege warning; playback continues without sync | Pending manual run |
| Two sessions | Start two different Navi media items | Each fixed HWND updates only its own media ID | Pending manual run |
| User-opened session | Open a file directly in PotPlayer while Navi has no session | Navi writes no watch history for it | Pending manual run |
| Shutdown | Exit Navi while a tracked item is playing | Pollers cancel and final save completes within the application shutdown timeout | Pending manual run |
