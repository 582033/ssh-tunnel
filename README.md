## 功能
* ssh隧道转发

## 用途
* 使用config.yaml中的配置将ssh连接转发至socks5,使需要联网的软件可以使用ssh作为代理进行联网


## 配置文件示例
```#yaml
# 服务器
username: "user" #服务器用户名
password: "password" #服务器密码
privateKey: "/Users/yjiang/.ssh/id_rsa" #私钥，绝对路径
serverAddr: "yjiang.cn" #服务器地址
serverPort: "22" #服务器端口
localPort: "1081" #本地socks5端口
chromePath: "/Applications/Google\ Chrome.app/Contents/MacOS/Google\ Chrome" #本地chrome路径
customDNS: "8.8.8.8:53" #自定义dns,不填则使用本地系统dns
useChrome: true  #是否使用本地chrome,false则为仅启动socks5代理
```

## 配置说明
* 默认调用了本地的chrome并使用参数进行启动: "--incognito", "--dns-prefetch-disable", "--single-process", "--proxy-server=socks5://localhost:"+GlobalConfig.LocalPort, "--user-data-dir=/tmp/chrome"

### 注意事项
* customDNS 参数根据dns污染情况进行调整
