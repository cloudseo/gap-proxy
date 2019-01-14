package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"
	"path"

	"fmt"

	"github.com/mitchellh/go-homedir"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

const (
	version    = "0.0.1"
	configDir  = ".gap-proxy"
	configFile = "config.json"
)

type config struct {
	ServerAddr string `json:"server"`
	LocalAddr  string `json:"local"`
	Key        string `json:"key"`
}

func workDir() string {
	dir, _ := homedir.Dir()
	return path.Join(dir, configDir)
}

func loadConf() (conf *config, err error) {
	message := "could not load config file"
	var buf []byte
	if buf, err = ioutil.ReadFile(path.Join(workDir(), configFile)); err != nil {
		err = errors.Wrapf(err, message)
		return
	}
	if err = json.Unmarshal(buf, &conf); err != nil {
		err = errors.Wrap(err, message)
	}
	return
}

func main() {
	log.SetFlags(0)

	var root = &cobra.Command{
		Use:           os.Args[0],
		Short:         "Gap is a secure socks5 proxy to speed up your network connection.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cobra.EnableCommandSorting = false
	log.SetOutput(os.Stdout)

	conf, err := loadConf()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	root.AddCommand(cmdLocal(conf))
	root.AddCommand(cmdServer(conf))
	root.AddCommand(cmdWnd())
	root.AddCommand(cmdVersion())

	var format = "%s\n"
	if os.Getenv(markEnvName) == markEnvValue {
		log.SetFlags(log.LstdFlags | log.Llongfile)
		format = "%+v\n"
	}

	if err := root.Execute(); err != nil {
		log.Printf(format, err)
	}
}
