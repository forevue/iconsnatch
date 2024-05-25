package main

import (
	"bytes"
	"errors"
	_ "faviconapi/ico"
	"faviconapi/iconpatch"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	_ "image/png"

	"os"
)

func main() {
	err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: [filename path]")
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}

	raw, _, err := image.Decode(f)
	if err != nil {
		f.Close()
		return err
	}

	f.Close()

	img := iconpatch.Patch(raw)

	var newBuf bytes.Buffer
	err = png.Encode(&newBuf, img)
	if err != nil {
		return err
	}

	//asBase64 := base64.StdEncoding.EncodeToString(newBuf.Bytes())
	//
	//
	//
	//err = exec.Command("firefox", "data:image/jpeg;base64,"+asBase64).Run()
	//if err != nil {
	//	return err
	//}

	_ = os.WriteFile("test.png", newBuf.Bytes(), 0777)

	return nil
}
