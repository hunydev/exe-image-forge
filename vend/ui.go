package main

import _ "embed"

//go:embed index.html
var indexHTML []byte

//go:embed admin.html
var adminHTML []byte

//go:embed passkey.js
var passkeyJS []byte

//go:embed favicon.svg
var faviconSVG []byte

// Third-party browser assets are embedded so authenticated admin pages never
// execute code from a CDN. Their license texts live beside the distributions.

//go:embed assets/xterm.js
var xtermJS []byte

//go:embed assets/xterm.css
var xtermCSS []byte

//go:embed assets/addon-fit.js
var addonFitJS []byte
