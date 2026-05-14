package main

const (
	productName    = "HarNest"
	cliCommandName = "harnest"
)

func cliErrorPrefix() string {
	return cliCommandName + ":"
}
