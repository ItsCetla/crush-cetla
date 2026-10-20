// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): shared/xml-unescape.ts, hooks/magic-context/inject-compartments.ts.

package magiccontext

import (
	"regexp"
	"strings"
)

var (
	xmlAttrEscaper    = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "'", "&apos;", "<", "&lt;", ">", "&gt;")
	xmlContentEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	xmlEntityRe       = regexp.MustCompile(`&(amp|apos|quot|lt|gt);`)
	xmlEntities       = map[string]string{"amp": "&", "apos": "'", "quot": `"`, "lt": "<", "gt": ">"}
)

func escapeXMLAttr(s string) string    { return xmlAttrEscaper.Replace(s) }
func escapeXMLContent(s string) string { return xmlContentEscaper.Replace(s) }

// unescapeXML decodes the five predefined entities in one left-to-right pass,
// so "&amp;lt;" becomes the text "&lt;", not "<".
func unescapeXML(s string) string {
	return xmlEntityRe.ReplaceAllStringFunc(s, func(e string) string {
		return xmlEntities[e[1:len(e)-1]]
	})
}
