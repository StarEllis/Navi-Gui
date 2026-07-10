package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const missingNFOFingerprint = "missing"

type UnsupportedNFOLayoutError struct {
	Reason string
}

func (e *UnsupportedNFOLayoutError) Error() string {
	if e == nil || strings.TrimSpace(e.Reason) == "" {
		return "UnsupportedNFOLayout"
	}
	return "UnsupportedNFOLayout: " + e.Reason
}

func unsupportedNFOLayout(reason string) error {
	return &UnsupportedNFOLayoutError{Reason: reason}
}

type nfoXMLElement struct {
	name       xml.Name
	start      int
	openEnd    int
	closeStart int
	end        int
	selfClose  bool
	children   []*nfoXMLElement
}

type nfoXMLDocument struct {
	data []byte
	root *nfoXMLElement
}

type nfoXMLPatch struct {
	start       int
	end         int
	replacement []byte
}

func parseNFOXMLDocument(data []byte, tolerateBareAmpersand bool) (*nfoXMLDocument, error) {
	parsed := data
	if tolerateBareAmpersand {
		if sanitized, changed := sanitizeMalformedNFOXML(data); changed {
			parsed = sanitized
		}
	}
	decoder := xml.NewDecoder(bytes.NewReader(parsed))
	var root *nfoXMLElement
	var stack []*nfoXMLElement
	for {
		before := int(decoder.InputOffset())
		token, err := decoder.Token()
		after := int(decoder.InputOffset())
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			element := &nfoXMLElement{name: typed.Name, start: before, openEnd: after}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("NFO contains multiple root elements")
				}
				root = element
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, element)
			}
			stack = append(stack, element)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("unexpected XML closing element %s", typed.Name.Local)
			}
			element := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			element.closeStart = before
			element.end = after
			opening := bytes.TrimSpace(parsed[element.start:element.openEnd])
			element.selfClose = bytes.HasSuffix(opening, []byte("/>"))
			if element.selfClose {
				element.closeStart = element.openEnd
				element.end = element.openEnd
			}
		case xml.CharData:
			if len(stack) == 0 && len(bytes.TrimSpace([]byte(typed))) != 0 {
				return nil, fmt.Errorf("NFO contains text outside the root element")
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("NFO XML has no root element")
	}
	if len(stack) != 0 {
		return nil, fmt.Errorf("NFO XML has unclosed elements")
	}
	return &nfoXMLDocument{data: parsed, root: root}, nil
}

func (d *nfoXMLDocument) directElements(names ...string) []*nfoXMLElement {
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[strings.ToLower(name)] = true
	}
	var result []*nfoXMLElement
	for _, child := range d.root.children {
		if child.name.Space == d.root.name.Space && wanted[strings.ToLower(child.name.Local)] {
			result = append(result, child)
		}
	}
	return result
}

func (d *nfoXMLDocument) fieldPresence() map[string]bool {
	presence := make(map[string]bool, len(d.root.children))
	for _, child := range d.root.children {
		if child.name.Space == d.root.name.Space {
			presence[strings.ToLower(child.name.Local)] = true
		}
	}
	return presence
}

func filterNFOXMLNamespaces(data []byte, tolerateBareAmpersand bool) ([]byte, error) {
	document, err := parseNFOXMLDocument(data, tolerateBareAmpersand)
	if err != nil {
		return nil, err
	}
	var patches []nfoXMLPatch
	var collect func(*nfoXMLElement)
	collect = func(parent *nfoXMLElement) {
		for _, child := range parent.children {
			if child.name.Space != parent.name.Space {
				patches = append(patches, nfoXMLPatch{start: child.start, end: child.end})
				continue
			}
			collect(child)
		}
	}
	collect(document.root)
	if len(patches) == 0 {
		return document.data, nil
	}
	return applyNFOXMLPatches(document.data, patches)
}

func (d *nfoXMLDocument) elementText(element *nfoXMLElement) (string, error) {
	if element == nil || element.selfClose {
		return "", nil
	}
	decoder := xml.NewDecoder(bytes.NewReader(d.data[element.start:element.end]))
	depth := 0
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return text.String(), nil
			}
			return "", err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 1 {
				text.Write([]byte(typed))
			}
		}
	}
}

func nfoXMLFieldPresence(data []byte) (map[string]bool, error) {
	document, err := parseNFOXMLDocument(data, true)
	if err != nil {
		return nil, err
	}
	return document.fieldPresence(), nil
}

func parseNFOActorMetadata(data []byte) (*NFOActorMetadata, error) {
	document, err := parseNFOXMLDocument(data, true)
	if err != nil {
		return nil, err
	}
	rootName := strings.ToLower(document.root.name.Local)
	if rootName != "movie" && rootName != "tvshow" {
		return nil, fmt.Errorf("unsupported NFO root element %q", document.root.name.Local)
	}

	actorElements := document.directElements("actor")
	explicitEmptyCollection := false
	for _, container := range document.directElements("actors") {
		var nestedActors []*nfoXMLElement
		for _, child := range container.children {
			if child.name.Space == container.name.Space && strings.EqualFold(child.name.Local, "actor") {
				nestedActors = append(nestedActors, child)
			}
		}
		if len(container.children) == 0 {
			text, textErr := document.elementText(container)
			if textErr != nil {
				return nil, textErr
			}
			if strings.TrimSpace(text) == "" {
				explicitEmptyCollection = true
			}
		}
		actorElements = append(actorElements, nestedActors...)
	}

	metadata := &NFOActorMetadata{}
	for _, element := range actorElements {
		actor, actorErr := document.parseActor(element)
		if actorErr != nil {
			return nil, actorErr
		}
		if actor.Name == "" {
			continue
		}
		metadata.Actors = append(metadata.Actors, actor)
	}
	for _, element := range document.directElements("director") {
		value, valueErr := document.elementText(element)
		if valueErr != nil {
			return nil, valueErr
		}
		if value = strings.TrimSpace(value); value != "" {
			metadata.Directors = append(metadata.Directors, value)
		}
	}
	metadata.ActorsPresent = len(metadata.Actors) > 0 || explicitEmptyCollection
	return metadata, nil
}

func (d *nfoXMLDocument) parseActor(element *nfoXMLElement) (NFOActor, error) {
	var actor NFOActor
	for _, child := range element.children {
		if child.name.Space != element.name.Space {
			continue
		}
		value, err := d.elementText(child)
		if err != nil {
			return NFOActor{}, err
		}
		switch strings.ToLower(child.name.Local) {
		case "name":
			actor.Name = strings.TrimSpace(value)
		case "role":
			actor.Role = strings.TrimSpace(value)
		case "thumb":
			actor.Thumb = strings.TrimSpace(value)
		case "sortorder":
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			parsed, parseErr := strconv.Atoi(value)
			if parseErr != nil {
				return NFOActor{}, fmt.Errorf("invalid actor sortorder %q: %w", value, parseErr)
			}
			actor.SortOrder = parsed
		}
	}
	return actor, nil
}

func nfoContentFingerprint(data []byte, exists bool) string {
	if !exists {
		return missingNFOFingerprint
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func escapeNFOText(value string) []byte {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(value))
	return output.Bytes()
}

func rawElementName(opening []byte) string {
	opening = bytes.TrimSpace(opening)
	if len(opening) == 0 || opening[0] != '<' {
		return ""
	}
	opening = opening[1:]
	end := bytes.IndexFunc(opening, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '/' || r == '>'
	})
	if end < 0 {
		return ""
	}
	return string(opening[:end])
}

func (d *nfoXMLDocument) replaceElementText(element *nfoXMLElement, value string) ([]byte, error) {
	if err := d.requireSimpleTextElement(element); err != nil {
		return nil, err
	}
	escaped := escapeNFOText(value)
	if !element.selfClose {
		replacement := make([]byte, 0, element.openEnd-element.start+len(escaped)+element.end-element.closeStart)
		replacement = append(replacement, d.data[element.start:element.openEnd]...)
		replacement = append(replacement, escaped...)
		replacement = append(replacement, d.data[element.closeStart:element.end]...)
		return replacement, nil
	}
	opening := bytes.TrimSpace(d.data[element.start:element.openEnd])
	name := rawElementName(opening)
	if name == "" {
		return nil, fmt.Errorf("unable to determine XML element name")
	}
	trimmed := bytes.TrimSuffix(opening, []byte("/>"))
	var replacement bytes.Buffer
	replacement.Write(trimmed)
	replacement.WriteByte('>')
	replacement.Write(escaped)
	replacement.WriteString("</")
	replacement.WriteString(name)
	replacement.WriteByte('>')
	return replacement.Bytes(), nil
}

func (d *nfoXMLDocument) requireSimpleTextElement(element *nfoXMLElement) error {
	if element == nil || element.selfClose {
		return nil
	}
	decoder := xml.NewDecoder(bytes.NewReader(d.data[element.openEnd:element.closeStart]))
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return unsupportedNFOLayout(fmt.Sprintf("cannot safely inspect <%s>: %v", element.name.Local, err))
		}
		if _, ok := token.(xml.CharData); !ok {
			return unsupportedNFOLayout(fmt.Sprintf("<%s> contains non-text XML content", element.name.Local))
		}
	}
}

func (d *nfoXMLDocument) expandSelfClosingRoot() ([]byte, error) {
	if d == nil || d.root == nil || !d.root.selfClose {
		return d.data, nil
	}
	opening := d.data[d.root.start:d.root.openEnd]
	closeAt := bytes.LastIndex(opening, []byte("/>"))
	name := rawElementName(opening)
	if closeAt < 0 || name == "" {
		return nil, unsupportedNFOLayout("cannot safely expand self-closing root element")
	}
	replacement := append([]byte(nil), opening[:closeAt]...)
	replacement = append(replacement, '>')
	replacement = append(replacement, []byte("</"+name+">")...)
	return applyNFOXMLPatches(d.data, []nfoXMLPatch{{
		start:       d.root.start,
		end:         d.root.end,
		replacement: replacement,
	}})
}

func renderNFOElement(name, value string) []byte {
	var output bytes.Buffer
	output.WriteByte('<')
	output.WriteString(name)
	output.WriteByte('>')
	output.Write(escapeNFOText(value))
	output.WriteString("</")
	output.WriteString(name)
	output.WriteByte('>')
	return output.Bytes()
}

func renderNFOActor(name string) []byte {
	var output bytes.Buffer
	output.WriteString("<actor><name>")
	output.Write(escapeNFOText(name))
	output.WriteString("</name></actor>")
	return output.Bytes()
}

func (d *nfoXMLDocument) newline() string {
	if bytes.Contains(d.data, []byte("\r\n")) {
		return "\r\n"
	}
	if bytes.Contains(d.data, []byte("\n")) {
		return "\n"
	}
	return ""
}

func (d *nfoXMLDocument) childIndent() string {
	if len(d.root.children) > 0 {
		start := d.root.children[0].start
		lineStart := bytes.LastIndexByte(d.data[:start], '\n') + 1
		prefix := d.data[lineStart:start]
		if len(bytes.Trim(prefix, " \t")) == 0 {
			return string(prefix)
		}
	}
	if d.newline() != "" {
		return "  "
	}
	return ""
}

func (d *nfoXMLDocument) separator() []byte {
	return []byte(d.newline() + d.childIndent())
}

func (d *nfoXMLDocument) elementIndent(element *nfoXMLElement) string {
	if element == nil {
		return d.childIndent()
	}
	lineStart := bytes.LastIndexByte(d.data[:element.start], '\n') + 1
	prefix := d.data[lineStart:element.start]
	if len(bytes.Trim(prefix, " \t")) == 0 {
		return string(prefix)
	}
	return d.childIndent()
}

func (d *nfoXMLDocument) rootInsertion(elements [][]byte) nfoXMLPatch {
	insertAt := d.root.closeStart
	newline := d.newline()
	indent := d.childIndent()
	if newline == "" {
		return nfoXMLPatch{start: insertAt, end: insertAt, replacement: bytes.Join(elements, nil)}
	}
	lineStart := bytes.LastIndexByte(d.data[:insertAt], '\n') + 1
	if len(bytes.Trim(d.data[lineStart:insertAt], " \t")) == 0 {
		insertAt = lineStart
	}
	var replacement bytes.Buffer
	if insertAt == d.root.closeStart {
		replacement.WriteString(newline)
	}
	for index, element := range elements {
		if index > 0 {
			replacement.WriteString(newline)
		}
		replacement.WriteString(indent)
		replacement.Write(element)
	}
	replacement.WriteString(newline)
	return nfoXMLPatch{start: insertAt, end: insertAt, replacement: replacement.Bytes()}
}

func applyNFOXMLPatches(data []byte, patches []nfoXMLPatch) ([]byte, error) {
	sort.SliceStable(patches, func(i, j int) bool {
		if patches[i].start == patches[j].start {
			return patches[i].end > patches[j].end
		}
		return patches[i].start < patches[j].start
	})
	lastEnd := 0
	var output bytes.Buffer
	for _, patch := range patches {
		if patch.start < lastEnd || patch.start < 0 || patch.end < patch.start || patch.end > len(data) {
			return nil, fmt.Errorf("overlapping or invalid XML patch")
		}
		output.Write(data[lastEnd:patch.start])
		output.Write(patch.replacement)
		lastEnd = patch.end
	}
	output.Write(data[lastEnd:])
	return output.Bytes(), nil
}

func normalizeNFOListValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validateUniqueNFOListIdentity(kind string, sourceValues, requestedValues []string) error {
	validate := func(values []string, side string) error {
		seen := make(map[string]bool, len(values))
		for _, value := range values {
			normalized := normalizeNFOListValue(value)
			if seen[normalized] {
				return unsupportedNFOLayout(fmt.Sprintf(
					"%s %s contains duplicate normalized value %q", kind, side, strings.TrimSpace(value)))
			}
			seen[normalized] = true
		}
		return nil
	}
	if err := validate(sourceValues, "XML"); err != nil {
		return err
	}
	return validate(requestedValues, "editor value")
}

func (d *nfoXMLDocument) directListValues(names ...string) ([]string, error) {
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[strings.ToLower(name)] = true
	}
	values := make([]string, 0)
	for _, child := range d.root.children {
		if !wanted[strings.ToLower(child.name.Local)] {
			continue
		}
		value, err := d.elementText(child)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (d *nfoXMLDocument) planScalar(name, value string, patches *[]nfoXMLPatch, inserts *[][]byte) error {
	existing := d.directElements(name)
	if len(existing) == 0 {
		*inserts = append(*inserts, renderNFOElement(name, value))
		return nil
	}
	if len(existing) > 1 {
		return unsupportedNFOLayout(fmt.Sprintf("multiple <%s> elements are ambiguous", name))
	}
	replacement, err := d.replaceElementText(existing[0], value)
	if err != nil {
		return err
	}
	*patches = append(*patches, nfoXMLPatch{start: existing[0].start, end: existing[0].end, replacement: replacement})
	return nil
}

func (d *nfoXMLDocument) planList(names []string, values []string, render func(string) []byte, patches *[]nfoXMLPatch, inserts *[][]byte) error {
	existing := d.directElements(names...)
	sourceValues, err := d.directListValues(names...)
	if err != nil {
		return err
	}
	if err := validateUniqueNFOListIdentity(strings.Join(names, "/"), sourceValues, values); err != nil {
		return err
	}
	for _, element := range existing {
		if err := d.requireSimpleTextElement(element); err != nil {
			return err
		}
	}
	used := make([]bool, len(existing))
	replacements := make([][]byte, 0, len(values))
	for _, value := range values {
		matched := -1
		for index, element := range existing {
			if used[index] {
				continue
			}
			existingValue, err := d.listElementValue(element)
			if err != nil {
				return err
			}
			if normalizeNFOListValue(existingValue) == normalizeNFOListValue(value) {
				matched = index
				break
			}
		}
		if matched >= 0 {
			used[matched] = true
			existingValue, err := d.listElementValue(existing[matched])
			if err != nil {
				return err
			}
			if strings.TrimSpace(existingValue) == strings.TrimSpace(value) {
				replacements = append(replacements, append([]byte(nil), d.data[existing[matched].start:existing[matched].end]...))
			} else {
				replacement, err := d.replaceElementText(existing[matched], value)
				if err != nil {
					return err
				}
				replacements = append(replacements, replacement)
			}
		} else {
			replacements = append(replacements, render(value))
		}
	}

	for index, element := range existing {
		var replacement []byte
		if index < len(replacements) {
			replacement = replacements[index]
		}
		*patches = append(*patches, nfoXMLPatch{start: element.start, end: element.end, replacement: replacement})
	}
	if len(replacements) > len(existing) {
		extra := replacements[len(existing):]
		if len(existing) == 0 {
			*inserts = append(*inserts, extra...)
		} else {
			*patches = append(*patches, nfoXMLPatch{
				start:       existing[len(existing)-1].end,
				end:         existing[len(existing)-1].end,
				replacement: append(d.separator(), bytes.Join(extra, d.separator())...),
			})
		}
	}
	return nil
}

func (d *nfoXMLDocument) planActors(values []string, patches *[]nfoXMLPatch, inserts *[][]byte) error {
	containers := d.directElements("actors")
	if len(containers) > 1 {
		return unsupportedNFOLayout("multiple <actors> containers are ambiguous")
	}
	topLevelActors := d.directElements("actor")
	var containedActors []*nfoXMLElement
	for _, container := range containers {
		for _, child := range container.children {
			if child.name.Space == container.name.Space && strings.EqualFold(child.name.Local, "actor") {
				containedActors = append(containedActors, child)
			}
		}
	}
	if len(topLevelActors) > 0 && len(containedActors) > 0 {
		return unsupportedNFOLayout("top-level <actor> elements cannot be mixed with an <actors> container")
	}
	existing := topLevelActors
	if len(containedActors) > 0 {
		existing = containedActors
	}
	actorNames := make([]*nfoXMLElement, len(existing))
	sourceValues := make([]string, len(existing))
	for index, element := range existing {
		name, err := d.actorNameElement(element)
		if err != nil {
			return err
		}
		actorNames[index] = name
		value, err := d.elementText(name)
		if err != nil {
			return err
		}
		sourceValues[index] = value
	}
	if err := validateUniqueNFOListIdentity("actor", sourceValues, values); err != nil {
		return err
	}

	used := make([]bool, len(existing))
	replacements := make([][]byte, 0, len(values))
	for _, value := range values {
		matched := -1
		for index, element := range existing {
			if used[index] {
				continue
			}
			existingValue, err := d.listElementValue(element)
			if err != nil {
				return err
			}
			if normalizeNFOListValue(existingValue) == normalizeNFOListValue(value) {
				matched = index
				break
			}
		}
		if matched >= 0 {
			used[matched] = true
			existingValue, err := d.elementText(actorNames[matched])
			if err != nil {
				return err
			}
			if strings.TrimSpace(existingValue) == strings.TrimSpace(value) {
				replacements = append(replacements, append([]byte(nil), d.data[existing[matched].start:existing[matched].end]...))
			} else {
				nameReplacement, err := d.replaceElementText(actorNames[matched], value)
				if err != nil {
					return err
				}
				actorReplacement := append([]byte(nil), d.data[existing[matched].start:actorNames[matched].start]...)
				actorReplacement = append(actorReplacement, nameReplacement...)
				actorReplacement = append(actorReplacement, d.data[actorNames[matched].end:existing[matched].end]...)
				replacements = append(replacements, actorReplacement)
			}
		} else {
			replacements = append(replacements, renderNFOActor(value))
		}
	}
	for index, element := range existing {
		var replacement []byte
		if index < len(replacements) {
			replacement = replacements[index]
		}
		*patches = append(*patches, nfoXMLPatch{start: element.start, end: element.end, replacement: replacement})
	}

	if len(replacements) > len(existing) {
		extra := replacements[len(existing):]
		switch {
		case len(existing) > 0:
			last := existing[len(existing)-1]
			separator := []byte(d.newline() + d.elementIndent(last))
			*patches = append(*patches, nfoXMLPatch{
				start:       last.end,
				end:         last.end,
				replacement: append(separator, bytes.Join(extra, separator)...),
			})
		case len(containers) > 0:
			container := containers[0]
			if container.selfClose {
				opening := bytes.TrimSuffix(bytes.TrimSpace(d.data[container.start:container.openEnd]), []byte("/>"))
				name := rawElementName(opening)
				var replacement bytes.Buffer
				replacement.Write(opening)
				replacement.WriteByte('>')
				separator := []byte(nil)
				if d.newline() != "" {
					separator = []byte(d.newline() + d.elementIndent(container) + "  ")
					replacement.Write(separator)
				}
				replacement.Write(bytes.Join(extra, separator))
				if d.newline() != "" {
					replacement.WriteString(d.newline() + d.elementIndent(container))
				}
				replacement.WriteString("</" + name + ">")
				*patches = append(*patches, nfoXMLPatch{start: container.start, end: container.end, replacement: replacement.Bytes()})
			} else {
				indent := d.elementIndent(container) + "  "
				if len(container.children) > 0 {
					indent = d.elementIndent(container.children[0])
				}
				separator := []byte(d.newline() + indent)
				insertion := bytes.Join(extra, separator)
				if d.newline() != "" {
					insertion = append(separator, insertion...)
				}
				*patches = append(*patches, nfoXMLPatch{start: container.closeStart, end: container.closeStart, replacement: insertion})
			}
		default:
			*inserts = append(*inserts, extra...)
		}
	}

	if len(values) == 0 {
		hasAuthoritativeEmptyContainer := false
		for _, container := range containers {
			hasNonActorChild := false
			for _, child := range container.children {
				if child.name.Space != container.name.Space || !strings.EqualFold(child.name.Local, "actor") {
					hasNonActorChild = true
					break
				}
			}
			if !hasNonActorChild {
				hasAuthoritativeEmptyContainer = true
				break
			}
		}
		if !hasAuthoritativeEmptyContainer {
			*inserts = append(*inserts, []byte("<actors/>"))
		}
	}
	return nil
}

func (d *nfoXMLDocument) actorNameElement(actor *nfoXMLElement) (*nfoXMLElement, error) {
	var names []*nfoXMLElement
	for _, child := range actor.children {
		if child.name.Space == actor.name.Space && strings.EqualFold(child.name.Local, "name") {
			names = append(names, child)
		}
	}
	if len(names) != 1 {
		return nil, unsupportedNFOLayout("actor must contain exactly one simple <name> element")
	}
	if err := d.requireSimpleTextElement(names[0]); err != nil {
		return nil, err
	}
	return names[0], nil
}

func (d *nfoXMLDocument) listElementValue(element *nfoXMLElement) (string, error) {
	if strings.EqualFold(element.name.Local, "actor") {
		for _, child := range element.children {
			if child.name.Space == element.name.Space && strings.EqualFold(child.name.Local, "name") {
				return d.elementText(child)
			}
		}
		return "", nil
	}
	return d.elementText(element)
}

func (d *nfoXMLDocument) planSeries(value string, patches *[]nfoXMLPatch, inserts *[][]byte) error {
	sets := d.directElements("set")
	if len(sets) > 1 {
		return unsupportedNFOLayout("multiple <set> elements are ambiguous")
	}
	if len(sets) == 0 {
		*inserts = append(*inserts, []byte("<set><name>"+string(escapeNFOText(value))+"</name></set>"))
	} else {
		set := sets[0]
		var name *nfoXMLElement
		for _, child := range set.children {
			if child.name.Space == set.name.Space && strings.EqualFold(child.name.Local, "name") {
				name = child
				break
			}
		}
		if name != nil {
			replacement, err := d.replaceElementText(name, value)
			if err != nil {
				return err
			}
			*patches = append(*patches, nfoXMLPatch{start: name.start, end: name.end, replacement: replacement})
		} else if set.selfClose {
			opening := bytes.TrimSuffix(bytes.TrimSpace(d.data[set.start:set.openEnd]), []byte("/>"))
			name := rawElementName(opening)
			replacement := append([]byte(nil), opening...)
			replacement = append(replacement, '>')
			replacement = append(replacement, []byte("<name>")...)
			replacement = append(replacement, escapeNFOText(value)...)
			replacement = append(replacement, []byte("</name></"+name+">")...)
			*patches = append(*patches, nfoXMLPatch{start: set.start, end: set.end, replacement: replacement})
		} else {
			insertion := []byte("<name>" + string(escapeNFOText(value)) + "</name>")
			*patches = append(*patches, nfoXMLPatch{start: set.closeStart, end: set.closeStart, replacement: insertion})
		}
	}
	return d.planScalar("series", value, patches, inserts)
}

func buildEditedNFO(original []byte, data *NFOEditorData) ([]byte, error) {
	document, err := parseNFOXMLDocument(original, false)
	if err != nil {
		return nil, fmt.Errorf("parse source NFO XML: %w", err)
	}
	rootName := strings.ToLower(document.root.name.Local)
	if rootName != "movie" && rootName != "tvshow" {
		return nil, fmt.Errorf("unsupported NFO root element %q", document.root.name.Local)
	}
	rootQName := rawElementName(document.data[document.root.start:document.root.openEnd])
	if strings.Contains(rootQName, ":") {
		return nil, unsupportedNFOLayout("prefixed root namespaces are not supported for safe editing")
	}
	if document.root.selfClose {
		expanded, expandErr := document.expandSelfClosingRoot()
		if expandErr != nil {
			return nil, expandErr
		}
		original = expanded
		document, err = parseNFOXMLDocument(original, false)
		if err != nil {
			return nil, fmt.Errorf("parse expanded source NFO XML: %w", err)
		}
	}

	var patches []nfoXMLPatch
	var inserts [][]byte
	for _, field := range data.UpdatedFields {
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "title":
			err = document.planScalar("title", strings.TrimSpace(data.Title), &patches, &inserts)
		case "code":
			err = document.planScalar("num", strings.TrimSpace(data.Code), &patches, &inserts)
		case "release_date":
			err = document.planScalar("releasedate", strings.TrimSpace(data.ReleaseDate), &patches, &inserts)
		case "director":
			err = document.planList([]string{"director"}, splitEditorValues(data.Director), func(value string) []byte {
				return renderNFOElement("director", value)
			}, &patches, &inserts)
		case "series":
			err = document.planSeries(strings.TrimSpace(data.Series), &patches, &inserts)
		case "publisher":
			err = document.planScalar("publisher", strings.TrimSpace(data.Publisher), &patches, &inserts)
		case "maker":
			err = document.planScalar("maker", strings.TrimSpace(data.Maker), &patches, &inserts)
		case "genres":
			err = document.planList([]string{"genre", "tag"}, splitEditorValues(data.Genres), func(value string) []byte {
				return renderNFOElement("genre", value)
			}, &patches, &inserts)
		case "actors":
			actors := splitEditorValues(data.Actors)
			err = document.planActors(actors, &patches, &inserts)
		case "plot":
			err = document.planScalar("plot", strings.TrimSpace(data.Plot), &patches, &inserts)
		case "runtime":
			value := strings.TrimSpace(data.Runtime)
			if value != "" {
				parsed, parseErr := strconv.Atoi(value)
				if parseErr != nil || parsed < 0 {
					return nil, fmt.Errorf("invalid runtime %q", data.Runtime)
				}
			}
			err = document.planScalar("runtime", value, &patches, &inserts)
		case "rating":
			value := strings.TrimSpace(data.Rating)
			if value != "" {
				parsed, parseErr := strconv.ParseFloat(value, 64)
				if parseErr != nil || parsed < 0 {
					return nil, fmt.Errorf("invalid rating %q", data.Rating)
				}
			}
			err = document.planScalar("rating", value, &patches, &inserts)
		}
		if err != nil {
			return nil, err
		}
	}
	if len(inserts) > 0 {
		patches = append(patches, document.rootInsertion(inserts))
	}
	return applyNFOXMLPatches(original, patches)
}

func (s *NFOService) validateNFOContent(data []byte) error {
	if s.validateNFO != nil {
		return s.validateNFO(data)
	}
	document, err := parseNFOXMLDocument(data, false)
	if err != nil {
		return err
	}
	rootName := strings.ToLower(document.root.name.Local)
	if rootName != "movie" && rootName != "tvshow" {
		return fmt.Errorf("unsupported NFO root element %q", document.root.name.Local)
	}
	return nil
}

func readNFOState(path string) ([]byte, os.FileInfo, bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, false, err
	}
	return content, info, true, nil
}

func (s *NFOService) writeNFOAtomically(path string, content []byte, mode os.FileMode, expectedFingerprint string) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return fmt.Errorf("create NFO directory: %w", err)
	}
	createTemp := s.createNFOtemp
	if createTemp == nil {
		createTemp = os.CreateTemp
	}
	temporary, err := createTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary NFO file: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()

	writeTemp := s.writeNFOtemp
	if writeTemp == nil {
		writeTemp = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	}
	written, writeErr := writeTemp(temporary, content)
	if writeErr != nil {
		return fmt.Errorf("write temporary NFO file: %w", writeErr)
	}
	if written != len(content) {
		return fmt.Errorf("write temporary NFO file: %w", io.ErrShortWrite)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("flush temporary NFO file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary NFO file: %w", err)
	}
	closed = true
	writtenContent, err := os.ReadFile(temporaryPath)
	if err != nil {
		return fmt.Errorf("read temporary NFO file for validation: %w", err)
	}
	if !bytes.Equal(writtenContent, content) {
		return fmt.Errorf("temporary NFO verification failed: written bytes differ")
	}
	if err := s.validateNFOContent(writtenContent); err != nil {
		return fmt.Errorf("validate generated NFO XML: %w", err)
	}
	if err := os.Chmod(temporaryPath, mode.Perm()); err != nil {
		return fmt.Errorf("preserve NFO permissions: %w", err)
	}

	current, _, exists, err := readNFOState(path)
	if err != nil {
		return fmt.Errorf("recheck NFO before replacement: %w", err)
	}
	if fingerprint := nfoContentFingerprint(current, exists); fingerprint != expectedFingerprint {
		return fmt.Errorf("NFO save conflict: source file changed during save")
	}
	replace := s.replaceNFO
	if replace == nil {
		replace = replaceNFOFileAtomic
	}
	if err := replace(temporaryPath, path); err != nil {
		return fmt.Errorf("replace NFO file atomically: %w", err)
	}
	return nil
}
