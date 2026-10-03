//go:build !embed_frontend

package main

import "io/fs"

func frontendFS() fs.FS {
	return nil
}
