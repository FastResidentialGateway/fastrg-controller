package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"fastrg-controller/internal/storage"

	"github.com/sirupsen/logrus"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	clientv3 "go.etcd.io/etcd/client/v3"
	"golang.org/x/crypto/bcrypt"
)

// Redirect HTTP to HTTPS
func RedirectToHTTPS(w http.ResponseWriter, r *http.Request) {
	host := r.Host

	if len(host) > 5 && host[len(host)-5:] == ":8080" {
		host = host[:len(host)-5] + ":8443"
	} else if len(host) > 5 && host[len(host)-5:] == ":8443" {
		// Already on 8443, do nothing
	} else {
		// No port specified, default to 8443
		host = host + ":8443"
	}

	httpsURL := "https://" + host + r.RequestURI
	http.Redirect(w, r, httpsURL, http.StatusMovedPermanently)
}

func getJWTSecret() string {
	if secret := os.Getenv("JWT_SECRET"); secret != "" {
		return secret
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		// Default for development environment
		return "super-secret-key"
	}
	return base64.StdEncoding.EncodeToString(bytes)
}

// PortMapping represents a single SNAT port forwarding rule
type PortMapping struct {
	Index string `json:"index" example:"0"`
	DIP   string `json:"dip" example:"192.168.3.2"`
	DPort string `json:"dport" example:"8080"`
	EPort string `json:"eport" example:"12345"`
}

// HSI config structure (Include PPPoE and DHCP settings)
type HSIConfig struct {
	UserID       string        `json:"user_id" example:"2"`
	VlanID       string        `json:"vlan_id" example:"2"`
	AccountName  string        `json:"account_name" example:"admin"`
	Password     string        `json:"password" example:"admin"`
	DHCPAddrPool string        `json:"dhcp_addr_pool" example:"192.168.3.100-192.168.3.200"`
	DHCPSubnet   string        `json:"dhcp_subnet" example:"255.255.255.0"`
	DHCPGateway  string        `json:"dhcp_gateway" example:"192.168.3.1"`
	PortMappings []PortMapping `json:"port-mapping,omitempty"`
}

// HSIMetadata represents the metadata for HSI configuration
type HSIMetadata struct {
	Node            string `json:"node" example:"node001"`
	ResourceVersion string `json:"resourceVersion" example:"1"`
	UpdatedBy       string `json:"updatedBy" example:"admin"`
	UpdatedAt       string `json:"updatedAt" example:"2024-01-01T00:00:00Z"`
	EnableStatus    string `json:"enableStatus" example:"disabled"`
}

// HSI config with metadata structure for etcd storage
type HSIConfigWithMetadata struct {
	Config   HSIConfig   `json:"config"`
	Metadata HSIMetadata `json:"metadata"`
}

// HSI dial/hangup request structure
type HSIActionRequest struct {
	NodeID string `json:"node_id" example:"node001"`
	UserID string `json:"user_id" example:"2"`
}

// UpdateSubscriberCount represents the request to update subscriber count
type UpdateSubscriberCount struct {
	SubscriberCount int `json:"subscriber_count" example:"100"`
}

// DnsRecord represents a static DNS record stored in etcd
type DnsRecord struct {
	Domain string `json:"domain" example:"www.fastrg.org"`
	IP     string `json:"ip" example:"192.168.201.10"`
	TTL    uint32 `json:"ttl" example:"30"`
}

// SubscriberCountMetadata represents metadata for subscriber count
type SubscriberCountMetadata struct {
	Node            string `json:"node"`
	ResourceVersion string `json:"resourceVersion"`
	UpdatedAt       string `json:"updatedAt"`
	UpdatedBy       string `json:"updatedBy"`
}

// SubscriberCountData represents the subscriber count data structure in etcd
type SubscriberCountData struct {
	Metadata        SubscriberCountMetadata `json:"metadata"`
	SubscriberCount string                  `json:"subscriber_count"`
}

type RestServer struct {
	etcd      *storage.EtcdClient
	jwtSecret []byte
}

func NewRestServer(etcd *storage.EtcdClient) *RestServer {
	return &RestServer{etcd: etcd, jwtSecret: []byte(getJWTSecret())}
}

// EtcdHealthCheck returns the health status of the service
// @Summary      Health check
// @Description  Check if the service and etcd connection are healthy
// @Tags         Health
// @Accept       json
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "Service is healthy"
// @Failure      503  {object}  map[string]interface{}  "Service is unhealthy"
// @Router       /health [get]
func (r *RestServer) EtcdHealthCheck(c *gin.Context) {
	// Test etcd connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := r.etcd.Client().Get(ctx, "health-check")
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "unhealthy",
			"error":  "etcd connection failed",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    "healthy",
		"timestamp": time.Now().Unix(),
	})
}

// ===== JWT related =====
func (r *RestServer) generateToken(username string) (string, error) {
	claims := jwt.MapClaims{
		"username": username,
		"exp":      time.Now().Add(2 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(r.jwtSecret)
}

func (r *RestServer) validateToken(tokenString string) (*jwt.Token, error) {
	return jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return r.jwtSecret, nil
	})
}

// Extract username from token
func (r *RestServer) getUserFromToken(tokenString string) (string, error) {
	token, err := r.validateToken(tokenString)
	if err != nil || !token.Valid {
		return "", fmt.Errorf("invalid token")
	}

	claims := token.Claims.(jwt.MapClaims)
	username, ok := claims["username"].(string)
	if !ok {
		return "", fmt.Errorf("username not found in token")
	}

	return username, nil
}

// Get next resource version for HSI config
func (r *RestServer) getNextResourceVersion(ctx context.Context, etcdKey string) (string, error) {
	resp, err := r.etcd.Client().Get(ctx, etcdKey)
	if err != nil {
		return "", err
	}

	if len(resp.Kvs) == 0 {
		// First time creation, start with version 1
		return "1", nil
	}

	// Parse existing config to get current version
	var existingConfig HSIConfigWithMetadata
	if err := json.Unmarshal(resp.Kvs[0].Value, &existingConfig); err != nil {
		// If can't parse metadata, assume it's old format, start with version 2
		return "2", nil
	}

	// Parse current version and increment
	currentVersion := existingConfig.Metadata.ResourceVersion
	if currentVersion == "" {
		return "2", nil
	}

	// Simple increment - parse as number and add 1
	var nextVersion int
	if _, err := fmt.Sscanf(currentVersion, "%d", &nextVersion); err != nil {
		return "2", nil
	}
	nextVersion++

	return fmt.Sprintf("%d", nextVersion), nil
}

// Check if VLAN is already in use by another user on the same node
func (r *RestServer) isVlanInUse(ctx context.Context, nodeId, vlanId, currentUserId string) (bool, string, error) {
	etcdHSIConfigKey := fmt.Sprintf("configs/%s/hsi/", nodeId)
	resp, err := r.etcd.Client().Get(ctx, etcdHSIConfigKey, clientv3.WithPrefix())
	if err != nil {
		return false, "", err
	}

	for _, kv := range resp.Kvs {
		var configWithMetadata HSIConfigWithMetadata
		var config HSIConfig

		if err := json.Unmarshal(kv.Value, &configWithMetadata); err == nil {
			config = configWithMetadata.Config
		} else {
			continue
		}

		// Check if this VLAN is used by a different user
		if config.VlanID == vlanId && config.UserID != currentUserId {
			return true, config.UserID, nil
		}
	}

	return false, "", nil
}

func (r *RestServer) isHSIConfigEnabled(ctx context.Context, nodeId, userId string) (string, error) {
	etcdKey := fmt.Sprintf("configs/%s/hsi/%s", nodeId, userId)
	resp, err := r.etcd.Client().Get(ctx, etcdKey)
	if err != nil {
		return "unknown", err
	}

	if len(resp.Kvs) == 0 {
		return "unknown", nil
	}

	var configWithMetadata HSIConfigWithMetadata
	if err := json.Unmarshal(resp.Kvs[0].Value, &configWithMetadata); err != nil {
		return "unknown", err
	}

	return configWithMetadata.Metadata.EnableStatus, nil
}

// AuthMiddleware with blacklist check for production
func (r *RestServer) AuthMiddlewareWithBlacklist() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Missing Authorization header"})
			c.Abort()
			return
		}

		token, err := r.validateToken(authHeader)
		if err != nil || !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			c.Abort()
			return
		}

		// Check if token is blacklisted
		blacklistKey := fmt.Sprintf("token_blacklist/%s", authHeader)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		resp, err := r.etcd.Client().Get(ctx, blacklistKey)
		if err != nil {
			// etcd error, reject request for security
			logrus.WithError(err).Error("Failed to check token blacklist")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication service unavailable"})
			c.Abort()
			return
		}

		if len(resp.Kvs) > 0 {
			// token is blacklisted
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token has been revoked"})
			c.Abort()
			return
		}

		c.Next()
	}
}

// ===== etcd operation =====
func (r *RestServer) getUserPassword(username string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	key := "users/" + username
	resp, err := r.etcd.Client().Get(ctx, key)
	if err != nil {
		return "", err
	}
	if len(resp.Kvs) == 0 {
		return "", nil
	}
	return string(resp.Kvs[0].Value), nil
}

// ===== REST Handlers =====

// LoginRequest represents the login request body
type LoginRequest struct {
	Username string `json:"username" example:"admin"`
	Password string `json:"password" example:"admin"`
}

// LoginResponse represents the login response
type LoginResponse struct {
	Token string `json:"token" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error string `json:"error" example:"error message"`
}

// MessageResponse represents a success message response
type MessageResponse struct {
	Message string `json:"message" example:"operation successful"`
}

// Login authenticates a user and returns a JWT token
// @Summary      User login
// @Description  Authenticate user with username and password, returns JWT token
// @Tags         Authentication
// @Accept       json
// @Produce      json
// @Param        request  body      LoginRequest  true  "Login credentials"
// @Success      200      {object}  LoginResponse
// @Failure      400      {object}  ErrorResponse
// @Failure      401      {object}  ErrorResponse
// @Failure      500      {object}  ErrorResponse
// @Router       /login [post]
func (r *RestServer) Login(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	hashedPassword, err := r.getUserPassword(req.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read from etcd"})
		return
	}
	if hashedPassword == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not found"})
		return
	}

	if bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(req.Password)) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}

	token, err := r.generateToken(req.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token})
}

// Register creates a new user account
// @Summary      Register new user
// @Description  Create a new user account with username and password
// @Tags         Authentication
// @Accept       json
// @Produce      json
// @Param        request  body      LoginRequest  true  "Registration credentials"
// @Success      200      {object}  MessageResponse
// @Failure      400      {object}  ErrorResponse
// @Failure      409      {object}  ErrorResponse  "Username already exists"
// @Failure      500      {object}  ErrorResponse
// @Router       /register [post]
func (r *RestServer) Register(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	// Basic validation
	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username and password are required"})
		return
	}

	// Check if user already exists
	existingPassword, err := r.getUserPassword(req.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check existing user"})
		return
	}
	if existingPassword != "" {
		c.JSON(http.StatusConflict, gin.H{"error": "Username already exists"})
		return
	}

	// Create new user (reuse AddUser logic)
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	_, err = r.etcd.Client().Put(context.Background(), "users/"+req.Username, string(hash))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "User registered successfully"})
}

// Logout invalidates the current user's token
// @Summary      User logout
// @Description  Invalidate the current JWT token by adding it to the blacklist
// @Tags         Authentication
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  MessageResponse
// @Failure      400  {object}  ErrorResponse
// @Failure      401  {object}  ErrorResponse
// @Failure      500  {object}  ErrorResponse
// @Router       /logout [post]
func (r *RestServer) Logout(c *gin.Context) {
	// Require current user's token
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No token provided"})
		return
	}

	// Parse and validate token
	token, err := r.validateToken(authHeader)
	if err != nil || !token.Valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		return
	}

	// Add token to blacklist in etcd
	blacklistKey := fmt.Sprintf("token_blacklist/%s", authHeader)

	// Calculate remaining TTL for token
	claims := token.Claims.(jwt.MapClaims)
	exp := int64(claims["exp"].(float64))
	ttl := exp - time.Now().Unix()

	if ttl > 0 {
		// Only add to blacklist if token is not expired
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Use etcd's TTL feature to automatically clean up blacklist entry after token expires
		lease, err := r.etcd.Client().Grant(ctx, ttl)
		if err != nil {
			logrus.WithError(err).Error("Failed to create lease for token blacklist")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to logout"})
			return
		}

		_, err = r.etcd.Client().Put(ctx, blacklistKey, "revoked", clientv3.WithLease(lease.ID))
		if err != nil {
			logrus.WithError(err).Error("Failed to add token to blacklist")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to logout"})
			return
		}

		logrus.Infof("Token added to blacklist: %s", authHeader[:20]+"...")
	}

	c.JSON(http.StatusOK, gin.H{"message": "Logged out successfully"})
}

// NodeInfo represents a node's key-value information
type NodeInfo struct {
	Key   string `json:"key" example:"nodes/abc123"`
	Value string `json:"value" example:"{\"node_uuid\":\"abc123\",\"ip\":\"192.168.10.10\",\"last_seen_time\":1700000000,\"status\":\"active\"}"`
}

// ListNodes returns all registered nodes
// @Summary      List all nodes
// @Description  Get a list of all registered nodes from etcd
// @Tags         Nodes
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   NodeInfo
// @Failure      500  {object}  ErrorResponse
// @Router       /nodes [get]
func (r *RestServer) ListNodes(c *gin.Context) {
	ctx := c.Request.Context()
	resp, err := r.etcd.Client().Get(ctx, "nodes/", clientv3.WithPrefix())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	nodes := []map[string]string{}
	for _, kv := range resp.Kvs {
		nodes = append(nodes, map[string]string{
			"key":   string(kv.Key),
			"value": string(kv.Value),
		})
	}
	c.JSON(http.StatusOK, nodes)
}

// UnregisterNode removes a node from the system
// @Summary      Unregister a node
// @Description  Remove a node from the system by its UUID
// @Tags         Nodes
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        uuid  path      string  true  "Node UUID"
// @Success      200   {object}  MessageResponse
// @Failure      400   {object}  ErrorResponse
// @Failure      404   {object}  ErrorResponse
// @Failure      500   {object}  ErrorResponse
// @Router       /nodes/{uuid} [delete]
func (r *RestServer) UnregisterNode(c *gin.Context) {
	nodeUuid := c.Param("uuid")
	if nodeUuid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node UUID is required"})
		return
	}

	// Check if node exists
	ctx := c.Request.Context()
	etcdKey := fmt.Sprintf("nodes/%s", nodeUuid)
	resp, err := r.etcd.Client().Get(ctx, etcdKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check node existence"})
		return
	}

	if len(resp.Kvs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	// Delete node info
	_, err = r.etcd.Client().Delete(ctx, etcdKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unregister node"})
		return
	}

	logrus.Infof("Node unregistered successfully: UUID=%s", nodeUuid)
	c.JSON(http.StatusOK, gin.H{"message": "Node unregistered successfully"})
}

// ===== User Management =====

// UsersListResponse represents the list of users
type UsersListResponse struct {
	Users []string `json:"users" example:"admin,user1,user2"`
}

// ListUsers returns all registered users
// @Summary      List all users
// @Description  Get a list of all registered usernames
// @Tags         Users
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  UsersListResponse
// @Failure      500  {object}  ErrorResponse
// @Router       /users [get]
func (r *RestServer) ListUsers(c *gin.Context) {
	ctx := c.Request.Context()
	resp, err := r.etcd.Client().Get(ctx, "users/", clientv3.WithPrefix())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	users := []string{}
	for _, kv := range resp.Kvs {
		users = append(users, string(kv.Key)[6:]) // remove "users/" prefix
	}
	c.JSON(http.StatusOK, gin.H{"users": users})
}

// AddUser creates a new user
// @Summary      Add a new user
// @Description  Create a new user with username and password
// @Tags         Users
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      LoginRequest  true  "User credentials"
// @Success      200      {object}  MessageResponse
// @Failure      400      {object}  ErrorResponse
// @Failure      500      {object}  ErrorResponse
// @Router       /users [post]
func (r *RestServer) AddUser(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	_, err = r.etcd.Client().Put(context.Background(), "users/"+req.Username, string(hash))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "User created"})
}

// DeleteUser removes a user from the system
// @Summary      Delete a user
// @Description  Remove a user by username
// @Tags         Users
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        username  path      string  true  "Username to delete"
// @Success      200       {object}  MessageResponse
// @Failure      500       {object}  ErrorResponse
// @Router       /users/{username} [delete]
func (r *RestServer) DeleteUser(c *gin.Context) {
	username := c.Param("username")
	_, err := r.etcd.Client().Delete(context.Background(), "users/"+username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "User deleted"})
}

// ===== HSI Management =====

// HSIUserIdsResponse represents the list of HSI user IDs
type HSIUserIdsResponse struct {
	UserIds []string `json:"user_ids" example:"user1,user2"`
}

func (r *RestServer) GetSubscriberCount(ctx context.Context, nodeId string) int {
	subscriberCount := -1
	countKey := fmt.Sprintf("user_counts/%s/", nodeId)
	countResp, countErr := r.etcd.Client().Get(ctx, countKey)
	if countErr != nil {
		// Log and continue without filtering
		logrus.WithError(countErr).Warnf("Failed to get subscriber count for node %s, proceeding without filtering", nodeId)
	} else if len(countResp.Kvs) > 0 {
		var countData SubscriberCountData
		if err := json.Unmarshal(countResp.Kvs[0].Value, &countData); err != nil {
			logrus.WithError(err).Warnf("Failed to unmarshal subscriber count for node %s, proceeding without filtering", nodeId)
		} else {
			// Parse subscriber_count string to int
			if n, err := strconv.Atoi(countData.SubscriberCount); err == nil {
				subscriberCount = n
			} else {
				logrus.WithError(err).Warnf("Invalid subscriber_count value '%s' for node %s, proceeding without filtering", countData.SubscriberCount, nodeId)
			}
		}
	}
	return subscriberCount
}

// GetHSIUserIds returns all HSI user IDs for a node
// @Summary      Get HSI user IDs
// @Description  Get a list of all HSI user IDs for a specific node
// @Tags         HSI Configuration
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId  path      string  true  "Node ID"
// @Success      200     {object}  HSIUserIdsResponse
// @Failure      400     {object}  ErrorResponse
// @Failure      500     {object}  ErrorResponse
// @Router       /config/{nodeId}/hsi/users [get]
func (r *RestServer) GetHSIUserIds(c *gin.Context) {
	nodeId := c.Param("nodeId")
	if nodeId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID is required"})
		return
	}

	etcdHSIConfigKey := fmt.Sprintf("configs/%s/hsi/", nodeId)
	ctx := c.Request.Context()
	resp, err := r.etcd.Client().Get(ctx, etcdHSIConfigKey, clientv3.WithPrefix())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get HSI user IDs"})
		return
	}

	// Default: no filtering. If we find a valid subscriber count, use it.
	subscriberCount := -1

	// Try to read subscriber count from etcd (user_counts/{nodeId}/)
	if subscriberCount = r.GetSubscriberCount(ctx, nodeId); subscriberCount < 0 {
		logrus.Infof("No valid subscriber count found for node %s, returning all user IDs", nodeId)
	}

	userIds := []string{}
	for _, kv := range resp.Kvs {
		// get user_id from key "configs/{nodeId}/hsi/{userId}"
		key := string(kv.Key)
		if len(key) > len(etcdHSIConfigKey) {
			userId := key[len(etcdHSIConfigKey):]
			// If subscriberCount >= 0, apply numeric filtering: skip if numeric(userId) > subscriberCount
			if subscriberCount >= 0 {
				if uidNum, err := strconv.Atoi(userId); err == nil {
					if uidNum > subscriberCount {
						// skip this userId
						continue
					}
				}
				// if userId is non-numeric, include it
			}
			userIds = append(userIds, userId)
		}
	}
	c.JSON(http.StatusOK, gin.H{"user_ids": userIds})
}

// GetHSIConfig returns the HSI configuration for a specific user on a node
// @Summary      Get HSI configuration
// @Description  Get the HSI configuration (PPPoE and DHCP settings) for a specific user on a node
// @Tags         HSI Configuration
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId  path      string  true  "Node ID"
// @Param        userId  path      string  true  "User ID"
// @Success      200     {object}  HSIConfigWithMetadata
// @Failure      400     {object}  ErrorResponse
// @Failure      404     {object}  ErrorResponse
// @Failure      500     {object}  ErrorResponse
// @Router       /config/{nodeId}/hsi/{userId} [get]
func (r *RestServer) GetHSIConfig(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")
	if nodeId == "" || userId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID and User ID are required"})
		return
	}

	subscriberCount := r.GetSubscriberCount(c.Request.Context(), nodeId)
	if subscriberCount < 0 {
		logrus.Infof("No valid subscriber count found for node %s, proceeding without filtering", nodeId)
	} else {
		if uidNum, err := strconv.Atoi(userId); err == nil {
			if uidNum > subscriberCount {
				c.JSON(http.StatusBadRequest, gin.H{"error": "User ID exceeds subscriber count"})
				return
			}
		}
	}

	ctx := c.Request.Context()
	etcdKey := fmt.Sprintf("configs/%s/hsi/%s", nodeId, userId)
	resp, err := r.etcd.Client().Get(ctx, etcdKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get HSI config"})
		return
	}

	if len(resp.Kvs) == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "HSI config not found"})
		return
	}

	var configWithMetadata HSIConfigWithMetadata
	if err := json.Unmarshal(resp.Kvs[0].Value, &configWithMetadata); err == nil {
		// New format, only return the config part to the frontend
		c.JSON(http.StatusOK, configWithMetadata)
		return
	}

	c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse HSI config"})
}

// CreateHSIConfig creates a new HSI configuration for a node
// @Summary      Create HSI configuration
// @Description  Create a new HSI configuration (PPPoE and DHCP settings) for a node
// @Tags         HSI Configuration
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId   path      string     true  "Node ID"
// @Param        request  body      HSIConfig  true  "HSI configuration"
// @Success      200      {object}  MessageResponse
// @Failure      400      {object}  ErrorResponse
// @Failure      409      {object}  ErrorResponse  "VLAN already in use"
// @Failure      500      {object}  ErrorResponse
// @Router       /config/{nodeId}/hsi [post]
func (r *RestServer) CreateHSIConfig(c *gin.Context) {
	nodeId := c.Param("nodeId")
	if nodeId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID is required"})
		return
	}

	var config HSIConfig
	if err := c.ShouldBindJSON(&config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	// Validate required fields
	if config.UserID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User ID is required"})
		return
	}
	if config.VlanID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "VLAN ID is required"})
		return
	}
	if config.AccountName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Account Name is required"})
		return
	}
	if config.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Password is required"})
		return
	}
	if config.DHCPAddrPool == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "DHCP Address Pool is required"})
		return
	}
	if config.DHCPSubnet == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "DHCP Subnet is required"})
		return
	}
	if config.DHCPGateway == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "DHCP Gateway is required"})
		return
	}

	ctx := c.Request.Context()

	subscriberCount := r.GetSubscriberCount(ctx, nodeId)
	if subscriberCount < 0 {
		logrus.Infof("No valid subscriber count found for node %s, proceeding without filtering", nodeId)
	} else {
		if uidNum, err := strconv.Atoi(config.UserID); err == nil {
			if uidNum > subscriberCount {
				c.JSON(http.StatusBadRequest, gin.H{"error": "User ID exceeds subscriber count"})
				return
			}
		}
	}

	// Check if VLAN is already in use by another user
	inUse, existingUserId, err := r.isVlanInUse(ctx, nodeId, config.VlanID, config.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check VLAN availability"})
		return
	}
	if inUse {
		c.JSON(http.StatusConflict, gin.H{
			"error": fmt.Sprintf("Input VLAN has been already used by other user: %s", existingUserId),
		})
		return
	}

	// Get current username
	authHeader := c.GetHeader("Authorization")
	username, err := r.getUserFromToken(authHeader)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Failed to get user from token"})
		return
	}

	// Get next resource version
	key := fmt.Sprintf("configs/%s/hsi/%s", nodeId, config.UserID)
	resourceVersion, err := r.getNextResourceVersion(ctx, key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get resource version"})
		return
	}

	// Create config with metadata
	configWithMetadata := HSIConfigWithMetadata{
		Config: config,
	}
	configWithMetadata.Metadata.Node = nodeId
	configWithMetadata.Metadata.ResourceVersion = resourceVersion
	configWithMetadata.Metadata.UpdatedBy = username
	configWithMetadata.Metadata.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	configWithMetadata.Metadata.EnableStatus = "disabled"

	etcdKey := fmt.Sprintf("configs/%s/hsi/%s", nodeId, config.UserID)

	configJSON, err := json.Marshal(configWithMetadata)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal config"})
		return
	}

	_, err = r.etcd.Client().Put(ctx, etcdKey, string(configJSON))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save HSI config"})
		return
	}

	logrus.Infof("HSI config created for node %s, user: %s, version: %s, by: %s",
		nodeId, config.UserID, resourceVersion, username)
	c.JSON(http.StatusOK, gin.H{"message": "HSI config created successfully"})
}

// UpdateHSIConfig updates an existing HSI configuration
// @Summary      Update HSI configuration
// @Description  Update an existing HSI configuration for a specific user on a node
// @Tags         HSI Configuration
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId   path      string     true  "Node ID"
// @Param        userId   path      string     true  "User ID"
// @Param        request  body      HSIConfig  true  "HSI configuration"
// @Success      200      {object}  MessageResponse
// @Failure      400      {object}  ErrorResponse
// @Failure      409      {object}  ErrorResponse  "VLAN already in use"
// @Failure      500      {object}  ErrorResponse
// @Router       /config/{nodeId}/hsi/{userId} [put]
func (r *RestServer) UpdateHSIConfig(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")
	if nodeId == "" || userId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID and User ID are required"})
		return
	}

	var config HSIConfig
	if err := c.ShouldBindJSON(&config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	// Validate required fields
	if config.UserID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User ID is required"})
		return
	}
	if config.VlanID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "VLAN ID is required"})
		return
	}
	if config.AccountName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Account Name is required"})
		return
	}
	if config.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Password is required"})
		return
	}
	if config.DHCPAddrPool == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "DHCP Address Pool is required"})
		return
	}
	if config.DHCPSubnet == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "DHCP Subnet is required"})
		return
	}
	if config.DHCPGateway == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "DHCP Gateway is required"})
		return
	}

	// Ensure userId in URL params matches UserID in request body
	if config.UserID != userId {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User ID mismatch"})
		return
	}

	ctx := c.Request.Context()

	subscriberCount := r.GetSubscriberCount(ctx, nodeId)
	if subscriberCount < 0 {
		logrus.Infof("No valid subscriber count found for node %s, proceeding without filtering", nodeId)
	} else {
		if uidNum, err := strconv.Atoi(config.UserID); err == nil {
			if uidNum > subscriberCount {
				c.JSON(http.StatusBadRequest, gin.H{"error": "User ID exceeds subscriber count"})
				return
			}
		}
	}

	// Check if VLAN is already in use by another user
	inUse, existingUserId, err := r.isVlanInUse(ctx, nodeId, config.VlanID, userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check VLAN availability"})
		return
	}
	if inUse {
		c.JSON(http.StatusConflict, gin.H{
			"error": fmt.Sprintf("Input VLAN has been already used by other user: %s", existingUserId),
		})
		return
	}

	// Get current username
	authHeader := c.GetHeader("Authorization")
	username, err := r.getUserFromToken(authHeader)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Failed to get user from token"})
		return
	}

	// Get next resource version
	etcdKey := fmt.Sprintf("configs/%s/hsi/%s", nodeId, userId)
	resourceVersion, err := r.getNextResourceVersion(ctx, etcdKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get resource version"})
		return
	}

	enableStatus, err := r.isHSIConfigEnabled(ctx, nodeId, userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get current config status"})
		return
	}

	// Create config with metadata
	configWithMetadata := HSIConfigWithMetadata{
		Config: config,
	}
	configWithMetadata.Metadata.Node = nodeId
	configWithMetadata.Metadata.ResourceVersion = resourceVersion
	configWithMetadata.Metadata.UpdatedBy = username
	configWithMetadata.Metadata.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	configWithMetadata.Metadata.EnableStatus = enableStatus

	configJSON, err := json.Marshal(configWithMetadata)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal config"})
		return
	}

	_, err = r.etcd.Client().Put(ctx, etcdKey, string(configJSON))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update HSI config"})
		return
	}

	logrus.Infof("HSI config updated for node %s, user: %s, version: %s, by: %s",
		nodeId, userId, resourceVersion, username)
	c.JSON(http.StatusOK, gin.H{"message": "HSI config updated successfully"})
}

// DeleteHSIConfig deletes an HSI configuration
// @Summary      Delete HSI configuration
// @Description  Delete an HSI configuration for a specific user on a node
// @Tags         HSI Configuration
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId  path      string  true  "Node ID"
// @Param        userId  path      string  true  "User ID"
// @Success      200     {object}  MessageResponse
// @Failure      400     {object}  ErrorResponse
// @Failure      404     {object}  ErrorResponse
// @Failure      500     {object}  ErrorResponse
// @Router       /config/{nodeId}/hsi/{userId} [delete]
func (r *RestServer) DeleteHSIConfig(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")
	if nodeId == "" || userId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID and User ID are required"})
		return
	}

	ctx := c.Request.Context()

	subscriberCount := r.GetSubscriberCount(ctx, nodeId)
	if subscriberCount < 0 {
		logrus.Infof("No valid subscriber count found for node %s, proceeding without filtering", nodeId)
	} else {
		if uidNum, err := strconv.Atoi(userId); err == nil {
			if uidNum > subscriberCount {
				c.JSON(http.StatusBadRequest, gin.H{"error": "User ID exceeds subscriber count"})
				return
			}
		}
	}

	etcdKey := fmt.Sprintf("configs/%s/hsi/%s", nodeId, userId)
	// Check if config exists
	resp, err := r.etcd.Client().Get(ctx, etcdKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check HSI config"})
		return
	}

	if len(resp.Kvs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "HSI config not found"})
		return
	}

	// Delete config
	_, err = r.etcd.Client().Delete(ctx, etcdKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete HSI config"})
		return
	}

	logrus.Infof("HSI config deleted for node %s, user: %s", nodeId, userId)
	c.JSON(http.StatusOK, gin.H{"message": "HSI config deleted successfully"})
}

// DialPPPoE sends a PPPoE dial command to a node
// @Summary      Dial PPPoE
// @Description  Send a PPPoE dial command to establish connection on a node
// @Tags         PPPoE
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      HSIActionRequest  true  "PPPoE dial request"
// @Success      200      {object}  MessageResponse
// @Failure      400      {object}  ErrorResponse
// @Failure      404      {object}  ErrorResponse
// @Failure      500      {object}  ErrorResponse
// @Router       /pppoe/dial [post]
func (r *RestServer) DialPPPoE(c *gin.Context) {
	var req HSIActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	if req.NodeID == "" || req.UserID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID and User ID are required"})
		return
	}

	ctx := c.Request.Context()

	subscriberCount := r.GetSubscriberCount(ctx, req.NodeID)
	if subscriberCount < 0 {
		logrus.Infof("No valid subscriber count found for node %s, proceeding without filtering", req.NodeID)
	} else {
		if uidNum, err := strconv.Atoi(req.UserID); err == nil {
			if uidNum > subscriberCount {
				c.JSON(http.StatusBadRequest, gin.H{"error": "User ID exceeds subscriber count"})
				return
			}
		}
	}

	// Check if HSI config exists
	configKey := fmt.Sprintf("configs/%s/hsi/%s", req.NodeID, req.UserID)
	resp, err := r.etcd.Client().Get(ctx, configKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check PPPoE config"})
		return
	}

	if len(resp.Kvs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "PPPoE config not found"})
		return
	}

	// Parse HSI config to get PPPoE related parameters
	var hsiConfig HSIConfig

	// Try to parse new format (with metadata)
	var configWithMetadata HSIConfigWithMetadata
	if err := json.Unmarshal(resp.Kvs[0].Value, &configWithMetadata); err == nil {
		// New format
		hsiConfig = configWithMetadata.Config
	} else {
		// Try to parse old format
		if err := json.Unmarshal(resp.Kvs[0].Value, &hsiConfig); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse HSI config"})
			return
		}
	}

	// Create dial command and store it in etcd for the node to execute
	commandKey := fmt.Sprintf("commands/%s/pppoe_dial_%s", req.NodeID, req.UserID)
	commandData := map[string]interface{}{
		"action":    "dial",
		"user_id":   req.UserID,
		"vlan":      hsiConfig.VlanID,
		"account":   hsiConfig.AccountName,
		"password":  hsiConfig.Password,
		"timestamp": time.Now().Unix(),
	}

	commandJSON, err := json.Marshal(commandData)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create command"})
		return
	}

	_, err = r.etcd.Client().Put(ctx, commandKey, string(commandJSON))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to send dial command"})
		return
	}

	logrus.Infof("PPPoE dial command sent to node %s for user %s", req.NodeID, req.UserID)
	c.JSON(http.StatusOK, gin.H{"message": "PPPoE dial command sent successfully"})
}

// HangupPPPoE sends a PPPoE hangup command to a node
// @Summary      Hangup PPPoE
// @Description  Send a PPPoE hangup command to disconnect on a node
// @Tags         PPPoE
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      HSIActionRequest  true  "PPPoE hangup request"
// @Success      200      {object}  MessageResponse
// @Failure      400      {object}  ErrorResponse
// @Failure      404      {object}  ErrorResponse
// @Failure      500      {object}  ErrorResponse
// @Router       /pppoe/hangup [post]
func (r *RestServer) HangupPPPoE(c *gin.Context) {
	var req HSIActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	if req.NodeID == "" || req.UserID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID and User ID are required"})
		return
	}

	ctx := c.Request.Context()

	subscriberCount := r.GetSubscriberCount(ctx, req.NodeID)
	if subscriberCount < 0 {
		logrus.Infof("No valid subscriber count found for node %s, proceeding without filtering", req.NodeID)
	} else {
		if uidNum, err := strconv.Atoi(req.UserID); err == nil {
			if uidNum > subscriberCount {
				c.JSON(http.StatusBadRequest, gin.H{"error": "User ID exceeds subscriber count"})
				return
			}
		}
	}

	// Check if HSI config exists
	configKey := fmt.Sprintf("configs/%s/hsi/%s", req.NodeID, req.UserID)
	resp, err := r.etcd.Client().Get(ctx, configKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check HSI config"})
		return
	}

	if len(resp.Kvs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "HSI config not found"})
		return
	}

	// Parse HSI config to get PPPoE related parameters
	var hsiConfig HSIConfig

	// Try to parse new format (with metadata)
	var configWithMetadata HSIConfigWithMetadata
	if err := json.Unmarshal(resp.Kvs[0].Value, &configWithMetadata); err == nil {
		// New format
		hsiConfig = configWithMetadata.Config
	} else {
		// Try to parse old format
		if err := json.Unmarshal(resp.Kvs[0].Value, &hsiConfig); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse HSI config"})
			return
		}
	}

	// Create hangup command and store it in etcd for the node to execute
	commandKey := fmt.Sprintf("commands/%s/pppoe_hangup_%s", req.NodeID, req.UserID)
	commandData := map[string]interface{}{
		"action":    "hangup",
		"user_id":   req.UserID,
		"vlan":      hsiConfig.VlanID,
		"account":   hsiConfig.AccountName,
		"password":  hsiConfig.Password,
		"timestamp": time.Now().Unix(),
	}

	commandJSON, err := json.Marshal(commandData)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create command"})
		return
	}

	_, err = r.etcd.Client().Put(ctx, commandKey, string(commandJSON))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to send hangup command"})
		return
	}

	logrus.Infof("PPPoE hangup command sent to node %s for user %s", req.NodeID, req.UserID)
	c.JSON(http.StatusOK, gin.H{"message": "PPPoE hangup command sent successfully"})
}

// UpdateNodeSubscriberCount updates the subscriber count for a node
// @Summary      Update Node Subscriber Count
// @Description  Update the subscriber count for a specific node
// @Tags         Nodes
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId  path      string  true  "Node ID"
// @Param        request body      UpdateSubscriberCount  true  "Subscriber count request"
// @Success      200     {object}  MessageResponse
// @Failure      400     {object}  ErrorResponse
// @Failure      500     {object}  ErrorResponse
// @Router       /nodes/:nodeId/subscriber-count [put]
func (r *RestServer) UpdateNodeSubscriberCount(c *gin.Context) {
	nodeId := c.Param("nodeId")
	if nodeId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID is required"})
		return
	}

	var req UpdateSubscriberCount
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	if req.SubscriberCount < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Subscriber count must be non-negative"})
		return
	}

	ctx := c.Request.Context()

	// Get current username
	authHeader := c.GetHeader("Authorization")
	username, err := r.getUserFromToken(authHeader)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Failed to get user from token"})
		return
	}

	// Get next resource version
	key := fmt.Sprintf("user_counts/%s/", nodeId)
	resourceVersion, err := r.getNextResourceVersion(ctx, key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get resource version"})
		return
	}

	countData := SubscriberCountData{}
	countData.SubscriberCount = fmt.Sprintf("%d", req.SubscriberCount)
	countData.Metadata.Node = nodeId
	countData.Metadata.ResourceVersion = resourceVersion
	countData.Metadata.UpdatedBy = username
	countData.Metadata.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	countJSON, err := json.Marshal(countData)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal config"})
		return
	}

	_, err = r.etcd.Client().Put(ctx, key, string(countJSON))
	if err != nil {
		logrus.WithError(err).Errorf("Failed to update subscriber count for node %s", nodeId)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update subscriber count"})
		return
	}

	logrus.Infof("Updated subscriber count for node %s to %d", nodeId, req.SubscriberCount)
	c.JSON(http.StatusOK, gin.H{
		"message":          "Subscriber count updated successfully",
		"node_id":          nodeId,
		"subscriber_count": req.SubscriberCount,
	})
}

// GetNodeSubscriberCount gets the subscriber count for a node
// @Summary      Get Node Subscriber Count
// @Description  Get the subscriber count for a specific node
// @Tags         Nodes
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId  path      string  true  "Node ID"
// @Success      200     {object}  MessageResponse
// @Failure      400     {object}  ErrorResponse
// @Failure      404     {object}  ErrorResponse
// @Failure      500     {object}  ErrorResponse
// @Router       /nodes/:nodeId/subscriber-count [get]
func (r *RestServer) GetNodeSubscriberCount(c *gin.Context) {
	nodeId := c.Param("nodeId")
	if nodeId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID is required"})
		return
	}

	ctx := c.Request.Context()

	// Get subscriber count from etcd
	key := fmt.Sprintf("user_counts/%s/", nodeId)
	resp, err := r.etcd.Client().Get(ctx, key)
	if err != nil {
		logrus.WithError(err).Errorf("Failed to get subscriber count for node %s", nodeId)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get subscriber count"})
		return
	}

	if len(resp.Kvs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Subscriber count not found"})
		return
	}

	// Parse as JSON format with metadata
	var countData SubscriberCountData
	if err := json.Unmarshal(resp.Kvs[0].Value, &countData); err != nil {
		logrus.WithError(err).Errorf("Failed to unmarshal subscriber count data %v for node %s", resp.Kvs, nodeId)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse subscriber count"})
		return
	}

	// Parse subscriber_count string to integer
	count := 0
	if n, parseErr := fmt.Sscanf(countData.SubscriberCount, "%d", &count); parseErr != nil || n != 1 {
		logrus.WithError(parseErr).Errorf("Failed to parse subscriber count '%s' from JSON for node %s", countData.SubscriberCount, nodeId)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse subscriber count"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"node_id":          nodeId,
		"subscriber_count": count,
	})
}

// FailedEventsResponse represents the response for failed events
type FailedEventsResponse struct {
	Events []map[string]interface{} `json:"events"`
}

// GetFailedEvents returns failed events for a specific node
// @Summary      Get failed events for a node
// @Description  Get a list of failed events for a specific node
// @Tags         Failed Events
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId  path      string  true  "Node ID"
// @Success      200     {object}  FailedEventsResponse
// @Failure      400     {object}  ErrorResponse
// @Failure      500     {object}  ErrorResponse
// @Router       /failed-events/{nodeId} [get]
func (r *RestServer) GetFailedEvents(c *gin.Context) {
	nodeId := c.Param("nodeId")
	if nodeId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID is required"})
		return
	}

	ctx := c.Request.Context()
	prefix := fmt.Sprintf("failed_events_history/%s/", nodeId)

	resp, err := r.etcd.Client().Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithSort(clientv3.SortByKey, clientv3.SortDescend))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get failed events"})
		return
	}

	events := []map[string]interface{}{}
	for _, kv := range resp.Kvs {
		var event map[string]interface{}
		if err := json.Unmarshal(kv.Value, &event); err != nil {
			logrus.WithError(err).Error("Failed to parse failed event")
			continue
		}
		events = append(events, event)
	}

	c.JSON(http.StatusOK, gin.H{"events": events})
}

// GetAllFailedEvents returns all failed events across all nodes
// @Summary      Get all failed events
// @Description  Get a list of all failed events across all nodes. Supports optional event_type filter.
// @Tags         Failed Events
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        event_type  query     string  false  "Filter by event type (e.g., pppoe_dial)"
// @Success      200         {object}  FailedEventsResponse
// @Failure      500         {object}  ErrorResponse
// @Router       /failed-events [get]
func (r *RestServer) GetAllFailedEvents(c *gin.Context) {
	ctx := c.Request.Context()
	prefix := "failed_events_history/"
	eventTypeFilter := c.Query("event_type") // Optional filter

	resp, err := r.etcd.Client().Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithSort(clientv3.SortByKey, clientv3.SortDescend))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get failed events"})
		return
	}

	events := []map[string]interface{}{}
	for _, kv := range resp.Kvs {
		var event map[string]interface{}
		if err := json.Unmarshal(kv.Value, &event); err != nil {
			logrus.WithError(err).Error("Failed to parse failed event")
			continue
		}

		// Apply event_type filter if specified
		if eventTypeFilter != "" {
			if eventType, ok := event["event_type"].(string); ok {
				if eventType != eventTypeFilter {
					continue // Skip events that don't match the filter
				}
			}
		}

		events = append(events, event)
	}

	c.JSON(http.StatusOK, gin.H{"events": events})
}

// ===== Static DNS Record Management =====

// GetDnsRecords returns all static DNS records for a user on a node
func (r *RestServer) GetDnsRecords(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")
	if nodeId == "" || userId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID and User ID are required"})
		return
	}

	ctx := c.Request.Context()
	prefix := fmt.Sprintf("configs/%s/%s/dns/", nodeId, userId)
	resp, err := r.etcd.Client().Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get DNS records"})
		return
	}

	records := []DnsRecord{}
	for _, kv := range resp.Kvs {
		var record DnsRecord
		if err := json.Unmarshal(kv.Value, &record); err != nil {
			logrus.WithError(err).Errorf("Failed to parse DNS record: %s", string(kv.Key))
			continue
		}
		records = append(records, record)
	}

	c.JSON(http.StatusOK, gin.H{"records": records})
}

// GetDnsRecord returns a single static DNS record by domain
func (r *RestServer) GetDnsRecord(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")
	domain := c.Param("domain")
	if nodeId == "" || userId == "" || domain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID, User ID and Domain are required"})
		return
	}

	ctx := c.Request.Context()
	key := fmt.Sprintf("configs/%s/%s/dns/%s", nodeId, userId, domain)
	resp, err := r.etcd.Client().Get(ctx, key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get DNS record"})
		return
	}

	if len(resp.Kvs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "DNS record not found"})
		return
	}

	var record DnsRecord
	if err := json.Unmarshal(resp.Kvs[0].Value, &record); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse DNS record"})
		return
	}

	c.JSON(http.StatusOK, record)
}

// AddOrUpdateDnsRecord creates or updates a static DNS record
func (r *RestServer) AddOrUpdateDnsRecord(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")
	if nodeId == "" || userId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID and User ID are required"})
		return
	}

	var record DnsRecord
	if err := c.ShouldBindJSON(&record); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	if record.Domain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Domain is required"})
		return
	}
	if record.IP == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "IP is required"})
		return
	}
	if record.TTL == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "TTL must be greater than 0"})
		return
	}

	ctx := c.Request.Context()
	key := fmt.Sprintf("configs/%s/%s/dns/%s", nodeId, userId, record.Domain)

	// Check if record already exists to determine add vs update
	existingResp, err := r.etcd.Client().Get(ctx, key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check existing DNS record"})
		return
	}
	isUpdate := len(existingResp.Kvs) > 0

	recordJSON, err := json.Marshal(record)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal DNS record"})
		return
	}

	_, err = r.etcd.Client().Put(ctx, key, string(recordJSON))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save DNS record"})
		return
	}

	if isUpdate {
		logrus.Infof("DNS record updated for node %s user %s domain %s", nodeId, userId, record.Domain)
		c.JSON(http.StatusOK, gin.H{"message": "DNS record updated successfully", "action": "updated"})
	} else {
		logrus.Infof("DNS record added for node %s user %s domain %s", nodeId, userId, record.Domain)
		c.JSON(http.StatusOK, gin.H{"message": "DNS record added successfully", "action": "added"})
	}
}

// DeleteDnsRecord removes a static DNS record
func (r *RestServer) DeleteDnsRecord(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")
	domain := c.Param("domain")
	if nodeId == "" || userId == "" || domain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID, User ID and Domain are required"})
		return
	}

	ctx := c.Request.Context()
	key := fmt.Sprintf("configs/%s/%s/dns/%s", nodeId, userId, domain)

	// Check if record exists
	resp, err := r.etcd.Client().Get(ctx, key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check DNS record"})
		return
	}
	if len(resp.Kvs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "DNS record not found"})
		return
	}

	_, err = r.etcd.Client().Delete(ctx, key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete DNS record"})
		return
	}

	logrus.Infof("DNS record deleted for node %s user %s domain %s", nodeId, userId, domain)
	c.JSON(http.StatusOK, gin.H{"message": "DNS record deleted successfully"})
}

func (r *RestServer) StartRestServer(addr string) error {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.SetTrustedProxies(nil)

	// ---- Security Headers Middleware ----
	router.Use(func(c *gin.Context) {
		c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Next()
	})

	// ---- API area ----
	api := router.Group("/api")
	{
		// Health check endpoint (no authentication required)
		api.GET("/health", r.EtcdHealthCheck)

		api.POST("/login", r.Login)
		api.POST("/register", r.Register) // Public registration endpoint
		api.POST("/logout", r.AuthMiddlewareWithBlacklist(), r.Logout)
		api.GET("/nodes", r.AuthMiddlewareWithBlacklist(), r.ListNodes)
		api.DELETE("/nodes/:uuid", r.AuthMiddlewareWithBlacklist(), r.UnregisterNode)
		api.GET("/nodes/:nodeId/subscriber-count", r.AuthMiddlewareWithBlacklist(), r.GetNodeSubscriberCount)
		api.PUT("/nodes/:nodeId/subscriber-count", r.AuthMiddlewareWithBlacklist(), r.UpdateNodeSubscriberCount)
		api.POST("/users", r.AuthMiddlewareWithBlacklist(), r.AddUser)
		api.DELETE("/users/:username", r.AuthMiddlewareWithBlacklist(), r.DeleteUser)
		api.GET("/users", r.AuthMiddlewareWithBlacklist(), r.ListUsers)

		// HSI route management
		api.GET("/config/:nodeId/hsi/users", r.AuthMiddlewareWithBlacklist(), r.GetHSIUserIds)
		api.GET("/config/:nodeId/hsi/:userId", r.AuthMiddlewareWithBlacklist(), r.GetHSIConfig)
		api.POST("/config/:nodeId/hsi", r.AuthMiddlewareWithBlacklist(), r.CreateHSIConfig)
		api.PUT("/config/:nodeId/hsi/:userId", r.AuthMiddlewareWithBlacklist(), r.UpdateHSIConfig)
		api.DELETE("/config/:nodeId/hsi/:userId", r.AuthMiddlewareWithBlacklist(), r.DeleteHSIConfig)
		api.POST("/pppoe/dial", r.AuthMiddlewareWithBlacklist(), r.DialPPPoE)
		api.POST("/pppoe/hangup", r.AuthMiddlewareWithBlacklist(), r.HangupPPPoE)

		// Static DNS record management
		api.GET("/config/:nodeId/dns/:userId", r.AuthMiddlewareWithBlacklist(), r.GetDnsRecords)
		api.GET("/config/:nodeId/dns/:userId/:domain", r.AuthMiddlewareWithBlacklist(), r.GetDnsRecord)
		api.POST("/config/:nodeId/dns/:userId", r.AuthMiddlewareWithBlacklist(), r.AddOrUpdateDnsRecord)
		api.DELETE("/config/:nodeId/dns/:userId/:domain", r.AuthMiddlewareWithBlacklist(), r.DeleteDnsRecord)

		// Failed events endpoints
		api.GET("/failed-events", r.AuthMiddlewareWithBlacklist(), r.GetAllFailedEvents)
		api.GET("/failed-events/:nodeId", r.AuthMiddlewareWithBlacklist(), r.GetFailedEvents)
	}

	// ---- Swagger API documentation ----
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// ---- Frontend React static files ----
	// Place static assets under /static to avoid catch-all conflicts with /api routes
	router.Static("/static", "./web/build/static")
	router.StaticFile("/favicon.ico", "./web/build/favicon.ico")
	// Root path returns index.html
	router.GET("/", func(c *gin.Context) {
		c.File("./web/build/index.html")
	})
	// Unmatched routes return index.html to let the frontend Router handle SPA paths
	router.NoRoute(func(c *gin.Context) {
		c.File("./web/build/index.html")
	})

	// ---- Start HTTPS server ----
	certFile := os.Getenv("CERT_FILE")
	if certFile == "" {
		certFile = "./certs/server.crt"
	}
	keyFile := os.Getenv("KEY_FILE")
	if keyFile == "" {
		keyFile = "./certs/server.key"
	}
	return router.RunTLS(addr, certFile, keyFile)
}

// Start HTTP redirect server
func StartHTTPRedirectServer(addr string) (*http.Server, error) {
	// Start HTTP server, redirecting to HTTPS
	logrus.Infof("HTTP redirect server starting on %s", addr)

	// Create HTTP server with redirect handler
	srv := &http.Server{
		Addr:    addr,
		Handler: http.HandlerFunc(RedirectToHTTPS),
	}

	go func() {
		logrus.Infof("HTTP redirect server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logrus.WithError(err).Error("HTTP redirect server failed")
		}
	}()

	return srv, nil
}
