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
