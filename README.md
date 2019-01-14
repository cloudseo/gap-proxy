# 简介
gap-proxy 是一个加速网络的 SOCKS5 安全代理工具，使用了专门设计的加密传输协议。

# mTCP
mTCP 是专门为 gap-proxy 设计的快速、可靠，加密安全、基于 UDP 的传输协议，借鉴了 KCP，TCP，Quic 的优点，技术特性：

* 支持连接迁移，在 IP 地址变化的情况下继续保持连接不断。
* 从协议头到应用数据都使用了 AES-256-CFB 加密。
* 类似 HMAC 机制，可防止数据被篡改。
* 选择确认，快速重传，快速超时选择重传。
* 简单的 KeepAlive，及时释放意外终止的连接。
* 简单快速的连接建立、连接释放。

mTCP 没有拥塞控制，只有流量控制。正因为如此，在高丢包率网络环境中，它才比使用了拥塞控制的 TCP 更快，即便是用了 BBR。

mTCP 的 RTO 不像 TCP 那样丢包就翻 2 倍，也不像 KCP 翻 1.5 倍，而是根本不翻倍。拥塞控制都去掉了，要流氓就要专注点。

# 支持平台
gap-proxy 仅支持 macOS, Linux, 其他类 Unix 理论上支持，但并未测试过。如果需要在其他类 Unix 上运行，需要自己安装 `go1.8+` 并自己编译 gap-proxy：

```
$ go build -o gap-proxy *.go
```

# 安装

**提示：为了简单，如下操作在个人电脑，国外服务器上可都一样。**

#### 下载
根据你所使用的操作系统从 [releases](https://github.com/fanpei91/gap/releases/) 下载相应已编译好的二进制文件压缩包。

#### 安装
首先解压程序包，进入该目录，把 `gap-proxy` 目录移动到用户主目录下，并命名为 `.gap-proxy`：

```
$ mv gap-proxy ~/.gap-proxy
```

接着把 `gap-proxy` 二进制文件移动到 `/usr/local/bin` 或其他在 `$PATH` 环境变量的目录里，并命名为 `gap`：

```
$ mv gap-proxy /usr/local/bin/gap
```

最后编辑 `~/.gap-proxy/config.json` 文件，配置相关参数，举例：

```
{
	"local":  "127.0.0.1:1186", # 客户端监听地址 (SOCKS5)
	"server": "8.8.8.8:2286",   # 服务器端监听地址
	"key":    "gap-proxy"       # 密钥
}
```

# 基本使用
#### 启动
一旦安装并配置好后，在个人电脑系统上执行：

```
$ gap local start
```

然后在国外服务器上执行：

```
$ gap server start
```

这样便分别启动了 `gap-local` 和 `gap-server` 后台进程。

#### 代理
gap-proxy 只是个简单的 SOCKS5 代理服务，无 PAC 机制，无内置 [gfwlist](https://github.com/gfwlist/gfwlist)。为了更方便科学上网，需要自己在浏览器安装代理管理插件。Chrome 浏览器推荐使用 [SwitchyOmega](https://chrome.google.com/webstore/detail/proxy-switchyomega/padekgcemlokbadohgkifijomclgjgif)。

一旦安装好代理管理插件，插件的 SOCKS5 地址填写为配置文件的 `local` 字段的地址。

现在，去冲浪吧！

# 高级使用
gap-proxy 的默认接收窗口为 256 个包，每个包最多能装 1420 字节数据，假设数据包传输每轮次需要 300 毫秒（RTT），一秒就有 3.3 个轮次，那么每秒最多能传输 256 * 1420 * 3.3 / 1024 = 1171 KB 有效数据，不包括重传，不包括协议头。

默认值 256 基本上能流畅观看 720p 的 youtube 视频。如果看更高清的视频会卡顿，只要带宽允许，可把接收窗口值逐渐调大，比如：

```
$ gap wnd 512
```

这样便把当前所有连接、新连接的接收窗口都设为 512，`gap-server` 就能通过协议头的 `Window` 字段知道新发送窗口值。

现在，每秒最多能传输 2343 KB 数据。



# 待办事项
* [Proxifier](https://www.proxifier.com/) / [ProxyCap](http://www.proxycap.com/) 的核心功能，强制代理不支持 SOCKS5 协议的程序

# 感谢及参考
* [netstack](https://github.com/google/netstack)
* [kcp-go](https://github.com/xtaci/kcp-go)
* [shadowsockts-go](https://github.com/shadowsocks/shadowsocks-go)
* [go-daemon](https://github.com/sevlyar/go-daemon)
* [v2ray-core](https://github.com/v2ray/v2ray-core)
* [quic-go](https://github.com/lucas-clemente/quic-go)

# 捐助
本人目前无任何经济收入，如果你喜欢 gap-proxy，希望你捐助支持开发、完善、优化。哪怕只有一毛钱，也是极大的鼓励，多谢！ ：）

| 支付宝 | 微信 |
|---|---|
| ![](./Alipay.png) | ![](./Wechat.png) |