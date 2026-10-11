package main

import (
	"context"
	"time"

	"github.com/surge-go/aurora"
	"github.com/surge-go/aurora/core/logger"
)

// ============================================================================
// 请求/响应结构
// ============================================================================

// CreateUserRequest 创建用户请求
type CreateUserRequest struct {
	Name  string `json:"name" binding:"required"`
	Email string `json:"email" binding:"required,email"`
	Age   int    `json:"age" binding:"gte=0,lte=150"`
}

// GetUserRequest 获取用户请求
type GetUserRequest struct {
	ID int `uri:"id"`
}

// UserResponse 用户响应
type UserResponse struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Age       int       `json:"age"`
	CreatedAt time.Time `json:"created_at"`
}

// ListUsersResponse 用户列表响应
type ListUsersResponse struct {
	Users []UserResponse `json:"users"`
	Total int            `json:"total"`
}

// ============================================================================
// 业务逻辑
// ============================================================================

// UserService 用户服务（模拟）
type UserService struct {
	users  map[int]*UserResponse
	nextID int
	log    *logger.Logger
}

func NewUserService(log *logger.Logger) *UserService {
	return &UserService{
		users:  make(map[int]*UserResponse),
		nextID: 1,
		log:    log,
	}
}

func (s *UserService) Create(req *CreateUserRequest) *UserResponse {
	user := &UserResponse{
		ID:        s.nextID,
		Name:      req.Name,
		Email:     req.Email,
		Age:       req.Age,
		CreatedAt: time.Now(),
	}
	s.users[s.nextID] = user
	s.nextID++

	// 记录日志
	s.log.Info("user created",
		logger.Int("user_id", user.ID),
		logger.String("name", user.Name),
	)

	return user
}

func (s *UserService) Get(id int) (*UserResponse, error) {
	user, ok := s.users[id]
	if !ok {
		return nil, aurora.ErrNotFound.WithMessage("user not found")
	}
	return user, nil
}

func (s *UserService) List() []*UserResponse {
	users := make([]*UserResponse, 0, len(s.users))
	for _, user := range s.users {
		users = append(users, user)
	}
	return users
}

// ============================================================================
// HTTP Handlers
// ============================================================================

// CreateUser 创建用户
func CreateUser(svc *UserService) *aurora.HandlerWithMeta {
	return aurora.Bind(func(c *aurora.Context, req *CreateUserRequest) (*UserResponse, error) {
		return svc.Create(req), nil
	})
}

// GetUser 获取用户
func GetUser(svc *UserService) *aurora.HandlerWithMeta {
	return aurora.Bind(func(c *aurora.Context, req *GetUserRequest) (*UserResponse, error) {
		return svc.Get(req.ID)
	})
}

// ListUsers 获取用户列表
func ListUsers(svc *UserService) *aurora.HandlerWithMeta {
	return aurora.BindR(func(c *aurora.Context) (*ListUsersResponse, error) {
		users := svc.List()
		result := make([]UserResponse, len(users))
		for i, user := range users {
			result[i] = *user
		}
		return &ListUsersResponse{
			Users: result,
			Total: len(result),
		}, nil
	})
}

// ============================================================================
// 健康检查
// ============================================================================

type HealthResponse struct {
	Status string `json:"status"`
	Time   string `json:"time"`
}

func HealthCheck() *aurora.HandlerWithMeta {
	return aurora.BindR(func(c *aurora.Context) (*HealthResponse, error) {
		return &HealthResponse{
			Status: "ok",
			Time:   time.Now().Format(time.RFC3339),
		}, nil
	})
}

// ============================================================================
// 主函数
// ============================================================================

func main() {
	ctx := context.Background()

	// 1. 初始化 Logger
	log, err := logger.New(&logger.Config{
		Level:  logger.LevelInfo,
		Format: logger.FormatJSON,
		Output: logger.OutputStdout,
	})
	if err != nil {
		panic(err)
	}
	defer log.Sync()

	log.Info("starting aurora example server")

	// 2. 创建业务服务
	userSvc := NewUserService(log)

	// 3. 创建 Aurora Server
	engine, err := aurora.New(aurora.Config{
		Addr:         ":8080",
		Mode:         "debug",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}, aurora.WithLogger(log))
	if err != nil {
		log.Fatal("failed to create server", logger.Error(err))
	}

	// 4. 注册路由
	engine.GET("/health", HealthCheck())
	engine.POST("/users", CreateUser(userSvc))
	engine.GET("/users/:id", GetUser(userSvc))
	engine.GET("/users", ListUsers(userSvc))

	// 5. 启动服务器
	log.Info("server listening", logger.String("addr", ":8080"))
	log.InfoText("Try these commands:")
	log.InfoText("  curl http://localhost:8080/health")
	log.InfoText("  curl -X POST http://localhost:8080/users -H 'Content-Type: application/json' -d '{\"name\":\"Alice\",\"email\":\"alice@example.com\",\"age\":25}'")
	log.InfoText("  curl http://localhost:8080/users/1")
	log.InfoText("  curl http://localhost:8080/users")

	if err := engine.Run(ctx); err != nil {
		log.Fatal("server error", logger.Error(err))
	}
}
