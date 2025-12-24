@echo off
echo === 分布式缓存动态扩缩容测试 ===

echo 1. 编译项目...
go build -o jincache.exe
if %errorlevel% neq 0 (
    echo 编译失败！
    pause
    exit /b 1
)
echo 编译成功！

echo.
echo 2. 启动说明：
echo    请在3个不同的命令行窗口中分别运行以下命令：
echo.
echo    窗口1: jincache.exe -port=8001 -api=true
echo    窗口2: jincache.exe -port=8002
echo    窗口3: jincache.exe -port=8003
echo.
echo 3. 测试API：
echo    curl "http://localhost:9999/api?key=Tom"
echo    curl "http://localhost:9999/api?key=Jack"
echo.
echo 4. 查看etcd中的节点：
echo    etcdctl get --prefix /jincache/nodes/
echo.
echo 5. 动态扩缩容测试：
echo    - 启动2个节点后观察etcd注册
echo    - 启动第3个节点观察数据迁移
echo    - Ctrl+C关闭节点观察故障检测
echo.
echo 注意：确保etcd运行在 192.168.59.132:2379
echo.
pause