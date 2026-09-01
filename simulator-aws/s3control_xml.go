package main

import (
	"encoding/xml"
	"io"
	"net/http"
	"strings"
)

// The S3 control plane stores several configurations whose bodies are deep,
// open-ended documents the client composes — a Storage Lens configuration, a
// Storage Lens group's filter, a Multi-Region Access Point's regions. What the
// service does with them is store them and hand them back unchanged, so the
// simulator holds the client's own document rather than a transcription of the
// model's nesting: a read returns exactly what the write sent, which is the
// property a client checks.

// s3ControlXMLNode is one element of a stored document: its name, attributes,
// character data, and children, in document order.
type s3ControlXMLNode struct {
	Name     string             `json:"name"`
	Attrs    map[string]string  `json:"attrs,omitempty"`
	Text     string             `json:"text,omitempty"`
	Children []s3ControlXMLNode `json:"children,omitempty"`
}

// UnmarshalXML reads any element into the node, keeping its children in order.
func (n *s3ControlXMLNode) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	n.Name = start.Name.Local
	if len(start.Attr) > 0 {
		n.Attrs = map[string]string{}
		for _, a := range start.Attr {
			if a.Name.Local == "xmlns" || a.Name.Space == "xmlns" {
				continue
			}
			n.Attrs[a.Name.Local] = a.Value
		}
		if len(n.Attrs) == 0 {
			n.Attrs = nil
		}
	}
	var text strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			var child s3ControlXMLNode
			if err := child.UnmarshalXML(d, t); err != nil {
				return err
			}
			n.Children = append(n.Children, child)
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if len(n.Children) == 0 {
				n.Text = strings.TrimSpace(text.String())
			}
			return nil
		}
	}
}

// MarshalXML writes the node back out under the name it was read as, unless
// the caller renamed it — a stored document is echoed under whatever element
// the reading operation declares.
func (n s3ControlXMLNode) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if n.Name != "" && start.Name.Local == "s3ControlXMLNode" {
		start.Name.Local = n.Name
	}
	start.Attr = nil
	for k, v := range n.Attrs {
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: k}, Value: v})
	}
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	if len(n.Children) == 0 {
		if n.Text != "" {
			if err := e.EncodeToken(xml.CharData(n.Text)); err != nil {
				return err
			}
		}
	} else {
		for _, child := range n.Children {
			if err := child.MarshalXML(e, xml.StartElement{Name: xml.Name{Local: child.Name}}); err != nil {
				return err
			}
		}
	}
	return e.EncodeToken(xml.EndElement{Name: start.Name})
}

// Child finds the first child with a name, which is how the handlers read the
// few fields they act on out of an otherwise opaque document.
func (n s3ControlXMLNode) Child(name string) (s3ControlXMLNode, bool) {
	for _, c := range n.Children {
		if c.Name == name {
			return c, true
		}
	}
	return s3ControlXMLNode{}, false
}

// ChildText is the character data of a named child, empty when absent.
func (n s3ControlXMLNode) ChildText(name string) string {
	c, ok := n.Child(name)
	if !ok {
		return ""
	}
	return c.Text
}

// SetChild replaces (or appends) a child holding one value, which is how a
// service-assigned field — an ARN, a creation time — joins the client's
// document before it is stored.
func (n *s3ControlXMLNode) SetChild(name, value string) {
	for i := range n.Children {
		if n.Children[i].Name == name {
			n.Children[i] = s3ControlXMLNode{Name: name, Text: value}
			return
		}
	}
	n.Children = append(n.Children, s3ControlXMLNode{Name: name, Text: value})
}

// s3ControlReadXMLBody reads the request's document. A request whose body is
// absent or malformed is rejected rather than stored as an empty document.
func s3ControlReadXMLBody(w http.ResponseWriter, r *http.Request, element string) (s3ControlXMLNode, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		s3ControlError(w, "MalformedXML", "the request body is missing a "+element, http.StatusBadRequest)
		return s3ControlXMLNode{}, false
	}
	var node s3ControlXMLNode
	if err := xml.Unmarshal(body, &node); err != nil {
		s3ControlError(w, "MalformedXML", "could not parse the request: "+err.Error(), http.StatusBadRequest)
		return s3ControlXMLNode{}, false
	}
	return node, true
}

// s3ControlWriteXMLElement writes a stored document back under the element the
// reading operation declares.
func s3ControlWriteXMLElement(w http.ResponseWriter, status int, element string, node s3ControlXMLNode) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	node.Name = element
	enc := xml.NewEncoder(w)
	_ = node.MarshalXML(enc, xml.StartElement{Name: xml.Name{Local: element}})
	_ = enc.Flush()
}
