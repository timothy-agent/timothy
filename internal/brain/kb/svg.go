package kb

import (
	"bytes"
	"encoding/xml"
)

const svgNamespace = "http://www.w3.org/2000/svg"

// isSVG reports whether data's root element, after any BOM, XML
// prolog, comments and doctype, is an svg element in the SVG namespace
// or in no namespace. Content type and file extension are not trusted:
// SVGs are often served as text/plain or text/xml.
func isSVG(data []byte) bool {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	for {
		tok, err := d.Token()
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return t.Name.Local == "svg" && (t.Name.Space == "" || t.Name.Space == svgNamespace)
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return false
			}
		}
	}
}
