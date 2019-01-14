package main

import (
	"fmt"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"syscall"

	"net"

	"github.com/pkg/errors"
)

const (
	defaultPerm  = os.FileMode(0640)
	markEnvName  = "_GAP_PROXY_DAEMON_"
	markEnvValue = "1"
)

type command byte

type commandFunc func(conn net.Conn)

var commandHandlers = make(map[command]commandFunc, 0)

type context struct {
	LogFile  string
	SockFile string
	WorkDir  string
	Env      []string
	Args     []string

	program  string
	logFile  *os.File
	listener net.Listener
}

func wasBorn() bool {
	return os.Getenv(markEnvName) == markEnvValue
}

func (ctx *context) reborn() (child *os.Process, err error) {
	if !wasBorn() {
		child, err = ctx.parent()
	} else {
		err = ctx.child()
	}
	return
}

func (ctx *context) release() {
	if ctx.listener == nil {
		return
	}

	ctx.listener.Close()
	os.Remove(ctx.SockFile)
}

func (ctx *context) send(cmd command, out []byte, argv ...[]byte) (err error) {
	var abs string
	if abs, err = filepath.Abs(ctx.WorkDir); err != nil {
		err = errors.WithStack(err)
		return
	}

	name := path.Join(abs, ctx.SockFile)
	var conn net.Conn
	if conn, err = net.Dial("unix", name); err != nil {
		err = errors.WithStack(err)
		return
	}
	defer conn.Close()

	conn.Write([]byte{byte(cmd)})
	for _, v := range argv {
		if _, err = conn.Write(v); err != nil {
			err = errors.WithStack(err)
			return
		}
	}
	if out != nil {
		if _, err = conn.Read(out); err != nil {
			err = errors.WithStack(err)
			return
		}
	}
	return
}

func (ctx *context) listen(cmd command, f commandFunc) {
	commandHandlers[cmd] = f
}

func (ctx *context) quit() (err error) {
	var process *os.Process
	if process, err = os.FindProcess(os.Getpid()); err != nil {
		err = errors.WithStack(err)
		return
	}

	if err = process.Signal(syscall.SIGTERM); err != nil {
		err = errors.WithStack(err)
	}

	return
}

func (ctx *context) wait(signals ...os.Signal) {
	ch := make(chan os.Signal, len(signals))
	signal.Notify(ch, signals...)

	<-ch
	signal.Stop(ch)

	return
}

func (ctx *context) absPath(name string) (p string, err error) {
	if p, err = filepath.Abs(ctx.WorkDir); err != nil {
		err = errors.WithStack(err)
		return
	}
	p = path.Join(p, name)
	return
}

func (ctx *context) parent() (child *os.Process, err error) {
	if err = ctx.prepare(); err != nil {
		return
	}

	if err = ctx.openLogFile(); err != nil {
		return
	}

	attr := &os.ProcAttr{
		Env: ctx.Env,
		Dir: ctx.WorkDir,
		Files: []*os.File{
			os.Stdin,    // fd = 0, stdin
			ctx.logFile, // fd = 1, stdout
			ctx.logFile, // fd = 2, stderr
		},
	}
	if child, err = os.StartProcess(ctx.program, ctx.Args, attr); err != nil {
		err = errors.WithStack(err)
		return
	}
	return
}

func (ctx *context) child() (err error) {
	var abs string
	if abs, err = filepath.Abs(ctx.WorkDir); err != nil {
		err = errors.WithStack(err)
		return
	}
	name := path.Join(abs, ctx.SockFile)
	if ctx.listener, err = net.Listen("unix", name); err != nil {
		err = errors.WithStack(err)
		return
	}

	go ctx.handleCommand()

	return
}

func (ctx *context) handleCommand() {
	defer ctx.listener.Close()

	for {
		buf := make([]byte, 1)
		var conn net.Conn
		var err error

		if conn, err = ctx.listener.Accept(); err != nil {
			ctx.quit()
			printLog(errors.WithStack(err))
			break
		}

		if _, err = conn.Read(buf); err != nil {
			printLog(errors.WithStack(err))
			break
		}

		command := command(buf[0])
		if f, ok := commandHandlers[command]; ok {
			f(conn)
		}
	}
}

func (ctx *context) prepare() (err error) {
	if ctx.program, err = os.Executable(); err != nil {
		err = errors.WithStack(err)
		return
	}

	if len(ctx.Env) == 0 {
		ctx.Env = os.Environ()
	}
	mark := fmt.Sprintf("%s=%s", markEnvName, markEnvValue)
	ctx.Env = append(ctx.Env, mark)

	args := ctx.Args
	ctx.Args = os.Args
	ctx.Args = append(ctx.Args, args...)
	return
}

func (ctx *context) openLogFile() (err error) {
	var name string
	if name, err = ctx.absPath(ctx.LogFile); err != nil {
		return
	}
	flag := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if ctx.logFile, err = os.OpenFile(name, flag, defaultPerm); err != nil {
		err = errors.WithStack(err)
		return
	}
	return
}
