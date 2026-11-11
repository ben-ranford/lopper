package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("lopper fixture v1.8.12")
		return
	}
	data, err := json.Marshal(os.Args[1:])
	if err != nil {
		panic(err)
	}
	root, err := os.OpenRoot(".")
	if err != nil {
		panic(err)
	}
	defer root.Close()
	if err := root.WriteFile("analysis-args.json", data, 0o600); err != nil {
		panic(err)
	}
	fmt.Println("analysis-fixture-ok")
}
