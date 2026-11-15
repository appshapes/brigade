package teamfile

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
)

// errRepoint is ToServer's refusal of a document it cannot rewrite
// faithfully. It carries nothing of the file.
var errRepoint = errors.New("teamfile: the team file could not be rewritten to name the server")

// A span is one member of the document's top-level object, as byte offsets
// into the file: its name, and its value.
type span struct {
	name                 string
	nameStart, nameEnd   int
	valueStart, valueEnd int
}

// ToServer answers the bytes the team file at path should hold after its
// team moved to a Brigade server (work item 328): the file as it is, read
// as Parse reads it, with `adapter` naming RestAdapter, `url` the
// server's, `certificate` the server's pin when certificate is not empty
// and gone when it is, and `publishable_key` gone, because a server has
// none. Nothing else changes: every other member keeps its place and its
// bytes, and so does the white space between members, so a file a person
// indented stays indented and the one line `team create` writes stays one
// line. A `certificate` the file did not have goes after `url`, written
// the way `url` is.
//
// This package still writes nothing: the caller writes what is answered.
// The result is parsed again before it is returned and must say what was
// asked: the server, its pin, no key, and the team the file named before.
// It must also be a file Parse would read: one that grew past MaxBytes,
// which a longer url and a pin can do to a file that was near it, is
// refused as too large, because every session of the checkout would refuse
// it once it was written. Anything else is a refusal that carries nothing
// of the file, and the caller writes nothing.
func ToServer(path, url, certificate string) ([]byte, error) {
	data, err := readCapped(path)
	if err != nil {
		return nil, err
	}
	return toServer(data, url, certificate)
}

// toServer is ToServer after the read.
func toServer(data []byte, url, certificate string) ([]byte, error) {
	before, err := parseBytes(FileName, data)
	if err != nil {
		return nil, err
	}
	members, err := topLevel(data)
	if err != nil || len(members) == 0 {
		return nil, errRepoint
	}
	adapterValue, err := json.Marshal(RestAdapter)
	if err != nil {
		return nil, errRepoint
	}
	urlValue, err := json.Marshal(url)
	if err != nil {
		return nil, errRepoint
	}
	pinValue, err := json.Marshal(certificate)
	if err != nil {
		return nil, errRepoint
	}
	hasPin := false
	for _, m := range members {
		hasPin = hasPin || m.name == "certificate"
	}

	// The document is what stands before its first member, the members with
	// what sets each off from the one before it, and what stands after the
	// last. A member that stays is copied with its own separator; one that
	// goes takes its separator with it; and the first member written needs
	// none, whichever it was in the file.
	var out bytes.Buffer
	out.Write(data[:members[0].nameStart])
	wrote := false
	for i, m := range members {
		value := data[m.valueStart:m.valueEnd]
		switch m.name {
		case "adapter":
			value = adapterValue
		case "url":
			value = urlValue
		case "certificate":
			if certificate == "" {
				continue
			}
			value = pinValue
		case "publishable_key":
			continue
		}
		if wrote {
			out.Write(data[members[i-1].valueEnd:m.nameStart])
		}
		out.Write(data[m.nameStart:m.valueStart])
		out.Write(value)
		wrote = true
		if m.name == "url" && certificate != "" && !hasPin {
			// The new member is set off from `url` as `url` is set off from
			// its neighbour, and its colon is spaced as url's is.
			out.Write(separator(data, members, i))
			out.WriteString(`"certificate"`)
			out.Write(data[m.nameEnd:m.valueStart])
			out.Write(pinValue)
		}
	}
	out.Write(data[members[len(members)-1].valueEnd:])

	if out.Len() > MaxBytes {
		return nil, refusal(FileName, ReasonTooLarge, "the rewritten team file would be larger than a team file can be")
	}
	after, err := parseBytes(FileName, out.Bytes())
	if err != nil || after.Adapter != RestAdapter || after.URL != url || after.Certificate != certificate ||
		after.PublishableKey != "" || after.TeamRef != before.TeamRef || after.TeamName != before.TeamName {
		return nil, errRepoint
	}
	return out.Bytes(), nil
}

// separator is what stands between member i and a neighbour: the comma and
// the white space after the member before it, or, for the first member,
// before the one after it. A document with one member has no neighbour to
// copy from, and a comma is enough.
func separator(data []byte, members []span, i int) []byte {
	switch {
	case i > 0:
		return data[members[i-1].valueEnd:members[i].nameStart]
	case len(members) > 1:
		return data[members[0].valueEnd:members[1].nameStart]
	}
	return []byte(",")
}

// topLevel reads the members of the document's top-level object in the
// order the file has them, each with where its name and its value stand.
func topLevel(data []byte) ([]span, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return nil, errRepoint
	}
	var members []span
	for dec.PeekKind() != '}' {
		name, err := dec.ReadValue()
		if err != nil || name.Kind() != '"' {
			return nil, errRepoint
		}
		var m span
		if err := json.Unmarshal(name, &m.name); err != nil {
			return nil, errRepoint
		}
		m.nameEnd = int(dec.InputOffset())
		m.nameStart = m.nameEnd - len(name)
		value, err := dec.ReadValue()
		if err != nil {
			return nil, errRepoint
		}
		m.valueEnd = int(dec.InputOffset())
		m.valueStart = m.valueEnd - len(value)
		members = append(members, m)
	}
	return members, nil
}
