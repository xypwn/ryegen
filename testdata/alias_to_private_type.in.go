// This covers quite an edge case, but one that can and should be accounted for.
//
// Example of this happening in the wild: https://pkg.go.dev/fyne.io/fyne/v2@v2.7.0/container#Scroll
package main

import "fmt"

type priv struct {
	A int
	B int
}

func (p *priv) SayHello() {
	fmt.Println("Hello!", p.A, p.B)
}

type Priv = priv
