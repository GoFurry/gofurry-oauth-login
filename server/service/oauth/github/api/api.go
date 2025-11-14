package api

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/GoFurry/gofurry-oauth-login/api/proto/githuboauth"
	"github.com/GoFurry/gofurry-oauth-login/common"
	"github.com/GoFurry/gofurry-oauth-login/common/log"
	cs "github.com/GoFurry/gofurry-oauth-login/common/service"
	"github.com/GoFurry/gofurry-oauth-login/common/util"
	"github.com/GoFurry/gofurry-oauth-login/env"
	"github.com/tidwall/gjson"
	"google.golang.org/grpc"
)

var Api *api

type api struct{}

func NewApi() *api {
	return &api{}
}

func init() {
	Api = NewApi()
}

var once = sync.Once{}

var grpcServer *grpc.Server

// 实例的 ip:port
var ip, port = env.GetServerConfig().Server.IPAddress, env.GetServerConfig().Server.Port

//const (
//	ip   = "127.0.0.1"
//	port = "50056"
//)

func (api *api) Init() {
	once.Do(func() {
	})

	// GitHub 密钥
	cfg := env.GetServerConfig()
	if cfg.Github.ClientId == "" || cfg.Github.ClientSecret == "" {
		log.Error("GitHub Client ID或Secret未配置")
	}

	// 监听端口
	lis, err := net.Listen("tcp", ":"+port) // gRPC服务端口
	if err != nil {
		log.Error("监听失败: %v", err)
	}

	// 创建gRPC服务器
	grpcServer = grpc.NewServer()

	githuboauth.RegisterGithubOAuthServiceServer(grpcServer, &githubOAuthServer{
		clientID:     cfg.Github.ClientId,
		clientSecret: cfg.Github.ClientSecret,
	})

	// 注册到etcd
	if err := cs.RegisterToEtcd(env.GetServerConfig().Etcd.EtcdKey, ip+":"+port); err != nil {
		log.Error("注册到etcd失败: %v", err)
	}

	log.Info("GitHub OAuth gRPC服务启动，端口" + port)
	if err := grpcServer.Serve(lis); err != nil {
		log.Error("服务启动失败: %v", err)
	}
}

func (api *api) Stop() {
	if grpcServer != nil {
		grpcServer.GracefulStop()
		log.Info("gRPC服务已关闭")
	}
}

// =========================== 实现gRPC服务接口 ===========================
// 请求头
const (
	USER_AGENT  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"
	APPLICATION = "application/json"
)

type githubOAuthServer struct {
	githuboauth.UnimplementedGithubOAuthServiceServer
	clientID     string
	clientSecret string
}

// GetAccessToken 获取访问令牌
func (s *githubOAuthServer) GetAccessToken(ctx context.Context, req *githuboauth.GetAccessTokenRequest) (*githuboauth.GetAccessTokenResponse, error) {
	if req.Code == "" {
		log.Info("GetAccessToken失败：code为空") // 新增日志
		return &githuboauth.GetAccessTokenResponse{Error: "code不能为空"}, nil
	}

	url := "https://github.com/login/oauth/access_token"
	params := map[string]string{
		"client_id":     s.clientID,
		"client_secret": s.clientSecret,
		"code":          req.Code,
	}
	headers := map[string]string{
		"User-Agent": common.USER_AGENT,
		"Accept":     "application/json", // GitHub默认返回text/plain 指定JSON格式
	}

	// 打印请求参数
	//log.Info("请求GitHub token：url=%s, params=%+v", url, params)

	respStr, httpErr := util.GetByHttpWithParams(
		url,
		headers,
		params,
		10*time.Second,
		&env.GetServerConfig().Proxy.Url, // 国内请求 github 需要代理加速
	)
	if httpErr != nil {
		log.Error("GitHub token请求失败：%v", httpErr)
		return &githuboauth.GetAccessTokenResponse{Error: "请求GitHub失败: " + httpErr.Error()}, nil
	}

	// 打印GitHub响应
	//log.Info("GitHub token响应：%s", respStr)

	// 解析access_token
	accessToken := gjson.Get(respStr, "access_token").String()
	if accessToken == "" {
		// 提取错误信息
		errMsg := gjson.Get(respStr, "error").String()
		if errMsg == "" {
			errMsg = "未返回access_token，响应内容：" + respStr
		}
		log.Info("解析token失败：%s", errMsg)
		return &githuboauth.GetAccessTokenResponse{Error: errMsg}, nil
	}

	log.Info("获取access_token成功：%s", accessToken[:10]+"...")
	return &githuboauth.GetAccessTokenResponse{AccessToken: accessToken}, nil
}

// GetUserInfo 获取用户信息
func (s *githubOAuthServer) GetUserInfo(ctx context.Context, req *githuboauth.GetUserInfoRequest) (*githuboauth.GetUserInfoResponse, error) {
	if req.AccessToken == "" {
		return &githuboauth.GetUserInfoResponse{Error: "access_token不能为空"}, nil
	}

	// 请求GitHub的用户信息端点
	url := "https://api.github.com/user"
	headers := map[string]string{
		"User-Agent":    USER_AGENT,
		"Accept":        APPLICATION,
		"Authorization": "token " + req.AccessToken,
	}

	respStr, httpErr := util.GetByHttpWithParams(
		url,
		headers,
		map[string]string{},
		10*time.Second,
		&env.GetServerConfig().Proxy.Url, // 国内请求 github 需要代理加速
	)
	if httpErr != nil {
		return &githuboauth.GetUserInfoResponse{Error: "请求用户信息失败: " + httpErr.Error()}, nil
	}

	// 解析用户信息
	userInfo := &githuboauth.UserInfo{
		Login:     gjson.Get(respStr, "login").String(),
		AvatarUrl: gjson.Get(respStr, "avatar_url").String(),
		Email:     gjson.Get(respStr, "email").String(),
		Name:      gjson.Get(respStr, "name").String(),
	}

	return &githuboauth.GetUserInfoResponse{UserInfo: userInfo}, nil
}
