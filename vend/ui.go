package main

import _ "embed"

//go:embed index.html
var indexHTML []byte

//go:embed admin.html
var adminHTML []byte
