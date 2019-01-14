package main

import (
	"fmt"
	"io"
	"os"
	"syscall"

	"strconv"

	"encoding/binary"

	"net"

	"github.com/fanpei91/gap/mtcp"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

const (
	commandQuit command = 1 + iota
	commandSetWindow
	commandGetWindow
)

var (
	ctxLocal = &context{
		LogFile:  "local.log",
		SockFile: "local.sock",
		WorkDir:  workDir(),
	}

	ctxServer = &context{
		LogFile:  "server.log",
		SockFile: "server.sock",
		WorkDir:  workDir(),
	}
)

func cmdLocal(conf *config) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "local",
		Short:         "Manage local proxy",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(cmdLocalStart(conf))
	cmd.AddCommand(cmdLocalStop(conf))
	return cmd
}

func cmdLocalStart(conf *config) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "start",
		Short:         "Start local proxy",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) (err error) {
		defer ctxLocal.release()

		message := "could not run local proxy"
		var child *os.Process

		if child, err = ctxLocal.reborn(); err != nil {
			err = errors.Wrap(err, message)
			return
		}
		if child != nil {
			return
		}

		go func() {
			proxy := newLocalProxy(conf.LocalAddr, conf.ServerAddr, conf.Key)
			ctxLocal.listen(commandSetWindow, func(conn net.Conn) {
				buf := make([]byte, 2)
				if _, err := io.ReadAtLeast(conn, buf, 2); err != nil {
					printLog(errors.WithStack(err))
					return
				}
				wnd := binary.BigEndian.Uint16(buf)
				proxy.setRcvWnd(wnd)
			})

			ctxLocal.listen(commandGetWindow, func(conn net.Conn) {
				buf := make([]byte, 2)
				binary.BigEndian.PutUint16(buf, proxy.getRcvWnd())
				if _, err = conn.Write(buf); err != nil {
					printLog(errors.WithStack(err))
				}
			})

			if err := proxy.listen(); err != nil {
				printLog(err)
				ctxLocal.quit()
			}
		}()

		ctxLocal.listen(commandQuit, func(conn net.Conn) {
			ctxLocal.quit()
		})

		ctxLocal.wait(syscall.SIGTERM, syscall.SIGKILL, syscall.SIGINT, syscall.SIGQUIT)

		return
	}
	return cmd
}

func cmdLocalStop(_ *config) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "stop",
		Short:         "Stop local proxy",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) (err error) {
		message := "could not stop local proxy"
		if err = ctxLocal.send(commandQuit, nil); err != nil {
			err = errors.Wrap(err, message)
		}
		return
	}
	return cmd
}

func cmdServer(conf *config) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "server",
		Short:         "Manage server",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(cmdServerStart(conf))
	cmd.AddCommand(cmdServerStop(conf))
	return cmd
}

func cmdServerStart(conf *config) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "start",
		Short:         "Start server",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) (err error) {
		defer ctxServer.release()

		message := "could not run server"
		var child *os.Process
		child, err = ctxServer.reborn()
		if err != nil {
			err = errors.Wrap(err, message)
			return
		}
		if child != nil {
			return
		}

		go func() {
			if err := newServer(conf.ServerAddr, conf.Key).listen(); err != nil {
				printLog(err)
				ctxServer.quit()
			}
		}()

		ctxServer.listen(commandQuit, func(conn net.Conn) {
			ctxServer.quit()
		})

		ctxServer.wait(syscall.SIGTERM, syscall.SIGKILL, syscall.SIGINT, syscall.SIGQUIT)
		return
	}
	return cmd
}

func cmdServerStop(_ *config) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "stop",
		Short:         "Stop server",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) (err error) {
		message := "could not stop server"
		if err = ctxServer.send(commandQuit, nil); err != nil {
			err = errors.Wrap(err, message)
		}
		return
	}
	return cmd
}

func cmdVersion() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "version",
		Short:         "Show version information",
		SilenceUsage:  true,
		SilenceErrors: true,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("version: %s\n", version)
		},
	}
	return cmd
}

func cmdWnd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "wnd [1-65535]",
		Short:         fmt.Sprintf("Set or show receive window size (in packets) (default: %d)", mtcp.DefaultRcvWnd),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			message := "could not set receive window"

			if len(args) == 0 {
				out := make([]byte, 2)
				if err = ctxLocal.send(commandGetWindow, out); err != nil {
					err = errors.Wrap(err, message)
					return
				}
				fmt.Printf("wnd: %d\n", binary.BigEndian.Uint16(out))
				return
			}
			var wnd int64
			if wnd, err = strconv.ParseInt(args[0], 10, 32); err != nil {
				err = errors.WithStack(err)
				cmd.Usage()
				return
			}
			if wnd > 0xffff {
				cmd.Usage()
				return
			}

			argv := make([]byte, 2)
			binary.BigEndian.PutUint16(argv, uint16(wnd))
			if err = ctxLocal.send(commandSetWindow, nil, argv); err != nil {
				err = errors.Wrap(err, message)
			}
			return err
		},
	}
	cmd.DisableFlagsInUseLine = true
	return cmd
}
