# Windows bind mount 会让文件在容器内表现为 world-writable（0777），
# MySQL 出于安全会直接忽略这类配置文件：
#   mysqld: [Warning] World-writable config file '...' is ignored.
# 结果是参数静默回落到默认值（buffer pool 128M、time_zone=SYSTEM），
# 测量环境失去可信性。因此把配置烧进镜像，并显式指定 0644 权限。

FROM mysql:8.4

COPY --chmod=0644 my.cnf /etc/mysql/conf.d/sentinel.cnf
