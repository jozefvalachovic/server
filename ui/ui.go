// Package ui holds shared static assets (CSS) used by the swagger docs,
// admin metrics, and admin cache UIs. Centralising the stylesheet here means
// design tokens, base styles, and component classes are defined once and
// embedded into the binary from a single source file.
package ui

import _ "embed"

//go:embed style.css
var StyleCSS []byte
