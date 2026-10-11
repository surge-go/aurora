# Aurora Framework Example

这是一个完整的 Aurora 框架使用示例，演示了如何构建一个用户管理 API。

## 功能特性

- ✅ RESTful API 设计
- ✅ 请求参数绑定与验证
- ✅ 统一的响应格式
- ✅ 结构化日志
- ✅ 错误处理
- ✅ 健康检查

## 项目结构

```
example/
├── main.go       # 主程序
└── README.md     # 本文档
```

## 快速开始

### 1. 启动服务

```bash
cd example
go run main.go
```

服务将在 `http://localhost:8080` 启动。

### 2. API 测试

#### 健康检查

```bash
curl http://localhost:8080/health
```

响应：
```json
{
  "code": 0,
  "data": {
    "status": "ok",
    "time": "2026-10-09T22:32:24+08:00"
  },
  "message": "success"
}
```

#### 创建用户

```bash
curl -X POST http://localhost:8080/users \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "Alice",
    "email": "alice@example.com",
    "age": 25
  }'
```

响应：
```json
{
  "code": 0,
  "data": {
    "id": 1,
    "name": "Alice",
    "email": "alice@example.com",
    "age": 25,
    "created_at": "2026-10-09T22:32:24.901613+08:00"
  },
  "message": "success"
}
```

#### 获取用户详情

```bash
curl http://localhost:8080/users/1
```

响应：
```json
{
  "code": 0,
  "data": {
    "id": 1,
    "name": "Alice",
    "email": "alice@example.com",
    "age": 25,
    "created_at": "2026-10-09T22:32:24.901613+08:00"
  },
  "message": "success"
}
```

#### 获取用户列表

```bash
curl http://localhost:8080/users
```

响应：
```json
{
  "code": 0,
  "data": {
    "users": [
      {
        "id": 1,
        "name": "Alice",
        "email": "alice@example.com",
        "age": 25,
        "created_at": "2026-10-09T22:32:24.901613+08:00"
      }
    ],
    "total": 1
  },
  "message": "success"
}
```

## 错误处理示例

### 验证错误（无效邮箱）

```bash
curl -X POST http://localhost:8080/users \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "Bob",
    "email": "invalid-email",
    "age": 25
  }'
```

响应：
```json
{
  "code": 1001,
  "data": null,
  "message": "请求无效"
}
```

### 验证错误（年龄超出范围）

```bash
curl -X POST http://localhost:8080/users \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "Charlie",
    "email": "charlie@example.com",
    "age": 200
  }'
```

响应：
```json
{
  "code": 1001,
  "data": null,
  "message": "请求无效"
}
```

### 资源不存在

```bash
curl http://localhost:8080/users/999
```

响应：
```json
{
  "code": 1005,
  "data": null,
  "message": "user not found"
}
```

## 代码亮点

### 1. 类型安全的 Handler

使用 `aurora.Bind` 实现类型安全的请求处理：

```go
func CreateUser(svc *UserService) *aurora.HandlerWithMeta {
    return aurora.Bind(func(c *aurora.Context, req *CreateUserRequest) (*UserResponse, error) {
        return svc.Create(req), nil
    })
}
```

### 2. 自动参数绑定

框架自动根据 HTTP 方法绑定参数：
- POST/PUT/PATCH: 绑定 JSON body + URI 参数
- GET/DELETE: 绑定 query + URI 参数

```go
type GetUserRequest struct {
    ID int `uri:"id"`  // 自动从 /users/:id 中提取
}

type CreateUserRequest struct {
    Name  string `json:"name" binding:"required"`
    Email string `json:"email" binding:"required,email"`
    Age   int    `json:"age" binding:"required,gte=0,lte=150"`
}
```

### 3. 统一错误处理

所有错误自动转换为统一的响应格式：

```go
return nil, aurora.ErrNotFound.WithMessage("user not found")
```

### 4. 结构化日志

内置 Logger 支持结构化日志：

```go
s.log.Info("user created",
    logger.Int("user_id", user.ID),
    logger.String("name", user.Name),
)
```

## 技术栈

- **框架**: Aurora (基于 Gin)
- **日志**: aurora/pkg/logger
- **验证**: go-playground/validator

## 扩展建议

1. **数据持久化**: 集成数据库（MySQL/PostgreSQL）
2. **缓存**: 添加 Redis 缓存
3. **认证授权**: 实现 JWT 认证
4. **API 文档**: 使用 Swagger 生成文档
5. **测试**: 添加单元测试和集成测试
6. **配置管理**: 使用 Viper 管理配置

## License

MIT
