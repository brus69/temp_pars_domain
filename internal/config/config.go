package config

type Config struct {
	Workers int
	Delay   int
	Retry   int
	DBPath  string
	LogDir  string
}
