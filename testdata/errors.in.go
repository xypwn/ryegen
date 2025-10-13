package main

import (
	"errors"
	"fmt"
)

func Fail() error {
	return errors.New("failed")
}

// array wrapper prevents error from getting treated
// like a function failure
func GetFailure() [1]error {
	return [1]error{Fail()}
}

func PrintError(x error) {
	fmt.Println("Got error:", x)
}
