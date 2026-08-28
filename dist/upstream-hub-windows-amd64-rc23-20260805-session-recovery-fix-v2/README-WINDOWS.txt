Upstream Hub Windows amd64

1. 填写 config.yaml 中的 PostgreSQL 连接信息、security.appSecret 和后台登录配置。
2. 在当前目录运行：

   .\upstream-hub.exe -config .\config.yaml

3. 浏览器访问：http://服务器IP:8418

说明：
- 前端页面和定时调度器已嵌入 upstream-hub.exe，无需单独启动。
- 只依赖 PostgreSQL，不需要 Redis。
- 数据库需要先创建；程序首次启动时会自动创建所需数据表。
- 公网使用时，请将 auth.enabled 设为 true，并设置强管理员密码。
