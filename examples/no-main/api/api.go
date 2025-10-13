package api

import "net/url"

func Add(x, y int) int {
	return x + y
}

func Urlencode(s string) string {
	return url.QueryEscape(s)
}
