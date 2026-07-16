package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"
	"fastrg-controller/internal/validation"

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

var (
	cachedJWTSecret string
	jwtSecretOnce   sync.Once
)

const (
	requestOpTimeout = 5 * time.Second
	// dummyPasswordHash is a pre-generated bcrypt.DefaultCost hash used to keep
	// missing-user and wrong-password login paths equivalent in bcrypt work.
	dummyPasswordHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
)

// boundedRequestCtx caps a handler operation while preserving request cancellation.
func boundedRequestCtx(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), requestOpTimeout)
}

// GetJWTSecret returns a process-local JWT secret for legacy paths and tests
// that do not have etcd. Production wiring must use ResolveJWTSecret so every
// replica shares the same value.
// This is computed once and cached to ensure both REST and gRPC use the same value.
func GetJWTSecret() string {
	jwtSecretOnce.Do(func() {
		if secret := os.Getenv("JWT_SECRET"); secret != "" {
			cachedJWTSecret = secret
			return
		}
		bytes := make([]byte, 32)
		if _, err := rand.Read(bytes); err != nil {
			cachedJWTSecret = "super-secret-key"
			return
		}
		cachedJWTSecret = base64.StdEncoding.EncodeToString(bytes)
	})
	return cachedJWTSecret
}

func getJWTSecret() string {
	return GetJWTSecret()
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
	UserID             string        `json:"user_id" example:"2"`
	VlanID             string        `json:"vlan_id" example:"2"`
	AccountName        string        `json:"account_name" example:"admin"`
	Password           string        `json:"password" example:"admin"`
	DHCPAddrPool       string        `json:"dhcp_addr_pool" example:"192.168.3.100-192.168.3.200"`
	DHCPSubnet         string        `json:"dhcp_subnet" example:"255.255.255.0"`
	DHCPGateway        string        `json:"dhcp_gateway" example:"192.168.3.1"`
	DNSProxyEnable     *bool         `json:"dns_proxy_enable,omitempty"`
	TCPConntrackEnable *bool         `json:"tcp_conntrack_enable,omitempty"`
	PortMappings       []PortMapping `json:"port-mapping,omitempty"`
	// DesireStatus is the PPPoE expected state ("connect" | "disconnect").
	// Only DialPPPoE/HangupPPPoE change it; ordinary config edits preserve it.
	DesireStatus string `json:"desire_status" example:"disconnect"`
}

// HSIMetadata represents the metadata for HSI configuration
type HSIMetadata struct {
	Node            string `json:"node" example:"node001"`
	ResourceVersion string `json:"resourceVersion" example:"1"`
	UpdatedBy       string `json:"updatedBy" example:"admin"`
	UpdatedAt       string `json:"updatedAt" example:"2024-01-01T00:00:00Z"`
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
	etcd           *storage.EtcdClient
	jwtSecret      []byte
	nodeMonitorMgr *NodeMonitorManager
	// database is the PostgreSQL projection. It remains nil until an optional
	// database is ready; the DB-backed endpoints then return 503.
	database atomic.Pointer[db.DB]
}

// NewRestServer constructs the REST server with an explicitly resolved signing
// secret. The optional form preserves the legacy no-etcd test path.
func NewRestServer(etcd *storage.EtcdClient, nmm *NodeMonitorManager, database *db.DB, jwtSecrets ...[]byte) *RestServer {
	var secret []byte
	if len(jwtSecrets) > 0 {
		secret = append([]byte(nil), jwtSecrets[0]...)
	} else {
		secret = []byte(getJWTSecret())
	}
	r := &RestServer{etcd: etcd, jwtSecret: secret, nodeMonitorMgr: nmm}
	r.SetDatabase(database)
	return r
}

// SetDatabase makes a PostgreSQL connection available to DB-backed handlers.
// It is safe to call while the REST server is serving requests.
func (r *RestServer) SetDatabase(database *db.DB) { r.database.Store(database) }

// Database returns the currently available PostgreSQL connection, if any.
func (r *RestServer) Database() *db.DB { return r.database.Load() }

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
	}, jwt.WithValidMethods([]string{"HS256"}))
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

// nextResourceVersion derives the display-only resourceVersion to stamp on the
// value about to be written, from the current stored value (nil for a new key).
// This is a human-readable audit counter only; concurrency control uses etcd's
// ModRevision via EtcdClient.CAS, not this number. Behaviour mirrors the old
// getNextResourceVersion: new key -> "1", unparseable/missing version -> "2".
func nextResourceVersion(current []byte) string {
	if len(current) == 0 {
		return "1"
	}

	var meta struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(current, &meta); err != nil {
		return "2"
	}
	if meta.Metadata.ResourceVersion == "" {
		return "2"
	}

	var n int
	if _, err := fmt.Sscanf(meta.Metadata.ResourceVersion, "%d", &n); err != nil {
		return "2"
	}
	return fmt.Sprintf("%d", n+1)
}

// Sentinel errors returned by CAS mutate closures so handlers can map them to
// the right HTTP status. Their messages double as the client-facing text.
var (
	errHSIConfigExists   = errors.New("HSI config already exists for this user")
	errHSIConfigNotFound = errors.New("HSI config not found")
	errDNSRecordNotFound = errors.New("DNS record not found")
	errDNSRecordLimit    = errors.New("DNS record limit reached: maximum 64 records allowed")
)

// PPPoE expected-state values for HSIConfig.DesireStatus. node watches this
// field and reconciles the link; controller never drives PPPoE directly.
const (
	desireStatusConnect    = "connect"
	desireStatusDisconnect = "disconnect"
)

// listVlanOwners returns the (user, vlan) pairs of every HSI config on a node,
// for the validation layer to detect VLAN conflicts.
func (r *RestServer) listVlanOwners(ctx context.Context, nodeId string) ([]validation.VlanOwner, error) {
	etcdHSIConfigKey := fmt.Sprintf("configs/%s/hsi/", nodeId)
	resp, err := r.etcd.Client().Get(ctx, etcdHSIConfigKey, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}

	owners := make([]validation.VlanOwner, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var configWithMetadata HSIConfigWithMetadata
		if err := json.Unmarshal(kv.Value, &configWithMetadata); err != nil {
			continue
		}
		owners = append(owners, validation.VlanOwner{
			UserID: configWithMetadata.Config.UserID,
			VlanID: configWithMetadata.Config.VlanID,
		})
	}
	return owners, nil
}

// respondValidationError maps a validation error to the matching HTTP status:
// a uniqueness conflict becomes 409, any other validation failure 400.
func respondValidationError(c *gin.Context, err error) {
	var ve *validation.Error
	if errors.As(err, &ve) && ve.Conflict {
		c.JSON(http.StatusConflict, gin.H{"error": ve.Error()})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}

// validatePathIDs rejects unsafe nodeId/userId route parameters before a
// handler can use them to construct an etcd key or query another subsystem.
func validatePathIDs() gin.HandlerFunc {
	return func(c *gin.Context) {
		if nodeID := c.Param("nodeId"); nodeID != "" {
			if err := validation.ValidateNodeID(nodeID); err != nil {
				respondValidationError(c, err)
				c.Abort()
				return
			}
		}
		if userID := c.Param("userId"); userID != "" {
			if err := validation.ValidateUserID(userID); err != nil {
				respondValidationError(c, err)
				c.Abort()
				return
			}
		}
		c.Next()
	}
}

// hsiConfigInput maps the REST HSIConfig into the transport-neutral validation
// input.
func hsiConfigInput(config HSIConfig) validation.HSIConfigInput {
	return validation.HSIConfigInput{
		UserID:       config.UserID,
		VlanID:       config.VlanID,
		AccountName:  config.AccountName,
		Password:     config.Password,
		DHCPAddrPool: config.DHCPAddrPool,
		DHCPSubnet:   config.DHCPSubnet,
		DHCPGateway:  config.DHCPGateway,
	}
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
		_ = bcrypt.CompareHashAndPassword([]byte(dummyPasswordHash), []byte(req.Password))
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
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

// VerifyPassword verifies the current authenticated user's password
// @Summary      Verify admin password
// @Description  Verify the currently logged-in user's password without changing the session
// @Tags         Authentication
// @Accept       json
// @Produce      json
// @Param        request  body      object  true  "Password to verify"
// @Success      200      {object}  MessageResponse
// @Failure      400      {object}  ErrorResponse
// @Failure      401      {object}  ErrorResponse
// @Router       /verify-password [post]
func (r *RestServer) VerifyPassword(c *gin.Context) {
	var req struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	if req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Password is required"})
		return
	}

	authHeader := c.GetHeader("Authorization")
	username, err := r.getUserFromToken(authHeader)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		return
	}

	hashedPassword, err := r.getUserPassword(username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read from etcd"})
		return
	}
	if hashedPassword == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User not found"})
		return
	}

	if bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(req.Password)) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid password"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Password verified"})
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
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		return
	}
	expVal, ok := claims["exp"].(float64)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		return
	}
	ttl := int64(expVal) - time.Now().Unix()

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
		logrus.WithError(err).Error("Failed to list nodes")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list nodes"})
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
// @Param        nodeId  path      string  true  "Node ID"
// @Success      200   {object}  MessageResponse
// @Failure      400   {object}  ErrorResponse
// @Failure      404   {object}  ErrorResponse
// @Failure      500   {object}  ErrorResponse
// @Router       /nodes/{nodeId} [delete]
func (r *RestServer) UnregisterNode(c *gin.Context) {
	nodeUuid := c.Param("nodeId")
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

// ClearInactiveNodesResponse is returned by the bulk inactive-node cleanup
type ClearInactiveNodesResponse struct {
	Message string `json:"message" example:"Inactive nodes cleared"`
	Deleted int    `json:"deleted" example:"3"`
}

// ClearInactiveNodes removes every node currently marked inactive
// @Summary      Clear all inactive nodes
// @Description  Delete every node whose status is "inactive" (e.g. evicted after heartbeat timeout)
// @Tags         Nodes
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  ClearInactiveNodesResponse
// @Failure      500  {object}  ErrorResponse
// @Router       /nodes/inactive [delete]
func (r *RestServer) ClearInactiveNodes(c *gin.Context) {
	ctx := c.Request.Context()
	resp, err := r.etcd.Client().Get(ctx, "nodes/", clientv3.WithPrefix())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list nodes"})
		return
	}

	deleted := 0
	for _, kv := range resp.Kvs {
		var nodeData map[string]interface{}
		if err := json.Unmarshal(kv.Value, &nodeData); err != nil {
			logrus.WithError(err).Errorf("Failed to unmarshal node data for key %s", kv.Key)
			continue
		}

		if status, _ := nodeData["status"].(string); status != "inactive" {
			continue
		}

		if _, err := r.etcd.Client().Delete(ctx, string(kv.Key)); err != nil {
			logrus.WithError(err).Errorf("Failed to delete inactive node %s", kv.Key)
			continue
		}

		// Stop any lingering monitor for this node
		if nodeUUID, ok := nodeData["node_uuid"].(string); ok && nodeUUID != "" && r.nodeMonitorMgr != nil {
			r.nodeMonitorMgr.StopMonitoring(nodeUUID)
		}
		deleted++
	}

	logrus.Infof("Cleared %d inactive node(s)", deleted)
	c.JSON(http.StatusOK, gin.H{"message": "Inactive nodes cleared", "deleted": deleted})
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
		logrus.WithError(err).Error("Failed to list users")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list users"})
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
// @Failure      409      {object}  ErrorResponse
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
	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username and password are required"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	ctx, cancel := boundedRequestCtx(c)
	defer cancel()

	key := "users/" + req.Username
	txnResp, err := r.etcd.Client().Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, string(hash))).
		Commit()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save user"})
		return
	}
	if !txnResp.Succeeded {
		c.JSON(http.StatusConflict, gin.H{"error": "User already exists"})
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
// @Failure      404       {object}  ErrorResponse
// @Failure      409       {object}  ErrorResponse
// @Failure      500       {object}  ErrorResponse
// @Router       /users/{username} [delete]
func (r *RestServer) DeleteUser(c *gin.Context) {
	username := c.Param("username")
	ctx, cancel := boundedRequestCtx(c)
	defer cancel()

	key := "users/" + username
	userResp, err := r.etcd.Client().Get(ctx, key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read user"})
		return
	}
	if len(userResp.Kvs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	countResp, err := r.etcd.Client().Get(ctx, "users/", clientv3.WithPrefix(), clientv3.WithCountOnly())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count users"})
		return
	}
	if countResp.Count == 1 {
		c.JSON(http.StatusConflict, gin.H{"error": "Cannot delete the last user"})
		return
	}

	// The count/delete race is accepted for this authenticated, single-operator
	// management path; concurrent deletes can still remove the final users.
	deleteResp, err := r.etcd.Client().Delete(ctx, key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete user"})
		return
	}
	if deleteResp.Deleted == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
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
	}
	if err := validation.CheckSubscriberCount(userId, subscriberCount); err != nil {
		respondValidationError(c, err)
		return
	}

	ctx := c.Request.Context()
	etcdKey := fmt.Sprintf("configs/%s/hsi/%s", nodeId, userId)
	resp, err := r.etcd.Client().Get(ctx, etcdKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get HSI config"})
		return
	}

	if len(resp.Kvs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "HSI config not found"})
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
	if err := validation.ValidateHSIConfig(hsiConfigInput(config)); err != nil {
		respondValidationError(c, err)
		return
	}
	if err := validation.ValidateUserID(config.UserID); err != nil {
		respondValidationError(c, err)
		return
	}

	ctx := c.Request.Context()

	subscriberCount := r.GetSubscriberCount(ctx, nodeId)
	if subscriberCount < 0 {
		logrus.Infof("No valid subscriber count found for node %s, proceeding without filtering", nodeId)
	}
	if err := validation.CheckSubscriberCount(config.UserID, subscriberCount); err != nil {
		respondValidationError(c, err)
		return
	}

	// Check if VLAN is already in use by another user
	owners, err := r.listVlanOwners(ctx, nodeId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check VLAN availability"})
		return
	}
	if err := validation.CheckVlanUnique(config.VlanID, config.UserID, owners); err != nil {
		respondValidationError(c, err)
		return
	}

	// Default boolean toggle fields to true for new configs
	trueVal := true
	config.DNSProxyEnable = &trueVal
	config.TCPConntrackEnable = &trueVal
	// New configs start disconnected; PPPoE is driven later via desire_status.
	config.DesireStatus = desireStatusDisconnect

	// Get current username
	authHeader := c.GetHeader("Authorization")
	username, err := r.getUserFromToken(authHeader)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Failed to get user from token"})
		return
	}

	// CAS-create: the Version==0 guard inside CAS rejects a concurrent create,
	// and the mutate closure rejects an already-existing config.
	etcdKey := fmt.Sprintf("configs/%s/hsi/%s", nodeId, config.UserID)
	var resourceVersion string
	err = r.etcd.CAS(ctx, etcdKey, func(current []byte) (storage.CASResult, error) {
		if current != nil {
			return storage.CASResult{}, errHSIConfigExists
		}
		resourceVersion = nextResourceVersion(current)

		configWithMetadata := HSIConfigWithMetadata{Config: config}
		configWithMetadata.Metadata.Node = nodeId
		configWithMetadata.Metadata.ResourceVersion = resourceVersion
		configWithMetadata.Metadata.UpdatedBy = username
		configWithMetadata.Metadata.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

		configJSON, err := json.Marshal(configWithMetadata)
		if err != nil {
			return storage.CASResult{}, err
		}
		return storage.CASResult{Value: configJSON}, nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errHSIConfigExists):
			c.JSON(http.StatusConflict, gin.H{"error": errHSIConfigExists.Error()})
		case errors.Is(err, storage.ErrCASConflict):
			c.JSON(http.StatusConflict, gin.H{"error": "Concurrent update conflict, please retry"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save HSI config"})
		}
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
	if err := validation.ValidateHSIConfig(hsiConfigInput(config)); err != nil {
		respondValidationError(c, err)
		return
	}
	if err := validation.ValidateUserID(config.UserID); err != nil {
		respondValidationError(c, err)
		return
	}

	// Ensure userId in URL params matches UserID in request body
	if err := validation.ValidateUserIDMatch(userId, config.UserID); err != nil {
		respondValidationError(c, err)
		return
	}

	ctx := c.Request.Context()

	subscriberCount := r.GetSubscriberCount(ctx, nodeId)
	if subscriberCount < 0 {
		logrus.Infof("No valid subscriber count found for node %s, proceeding without filtering", nodeId)
	}
	if err := validation.CheckSubscriberCount(config.UserID, subscriberCount); err != nil {
		respondValidationError(c, err)
		return
	}

	// Check if VLAN is already in use by another user
	owners, err := r.listVlanOwners(ctx, nodeId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check VLAN availability"})
		return
	}
	if err := validation.CheckVlanUnique(config.VlanID, userId, owners); err != nil {
		respondValidationError(c, err)
		return
	}

	// Get current username
	authHeader := c.GetHeader("Authorization")
	username, err := r.getUserFromToken(authHeader)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Failed to get user from token"})
		return
	}

	// Default boolean toggle fields to true if not provided
	if config.DNSProxyEnable == nil {
		trueVal := true
		config.DNSProxyEnable = &trueVal
	}
	if config.TCPConntrackEnable == nil {
		trueVal := true
		config.TCPConntrackEnable = &trueVal
	}

	// CAS-update (upsert): read the current value inside the mutate so the
	// existing desire_status is preserved atomically. An ordinary config edit
	// must never change desire_status — only DialPPPoE/HangupPPPoE do — so any
	// desire_status sent in the request body is ignored.
	etcdKey := fmt.Sprintf("configs/%s/hsi/%s", nodeId, userId)
	var resourceVersion string
	err = r.etcd.CAS(ctx, etcdKey, func(current []byte) (storage.CASResult, error) {
		desire := desireStatusDisconnect
		if current != nil {
			var existing HSIConfigWithMetadata
			if err := json.Unmarshal(current, &existing); err == nil && existing.Config.DesireStatus != "" {
				desire = existing.Config.DesireStatus
			}
		}
		config.DesireStatus = desire
		resourceVersion = nextResourceVersion(current)

		configWithMetadata := HSIConfigWithMetadata{Config: config}
		configWithMetadata.Metadata.Node = nodeId
		configWithMetadata.Metadata.ResourceVersion = resourceVersion
		configWithMetadata.Metadata.UpdatedBy = username
		configWithMetadata.Metadata.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

		configJSON, err := json.Marshal(configWithMetadata)
		if err != nil {
			return storage.CASResult{}, err
		}
		return storage.CASResult{Value: configJSON}, nil
	})
	if err != nil {
		if errors.Is(err, storage.ErrCASConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "Concurrent update conflict, please retry"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update HSI config"})
		}
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
	}
	if err := validation.CheckSubscriberCount(userId, subscriberCount); err != nil {
		respondValidationError(c, err)
		return
	}

	// CAS-delete: the mutate closure confirms existence; the Txn guard ensures
	// the delete only lands if nobody changed the config in between.
	etcdKey := fmt.Sprintf("configs/%s/hsi/%s", nodeId, userId)
	err := r.etcd.CAS(ctx, etcdKey, func(current []byte) (storage.CASResult, error) {
		if current == nil {
			return storage.CASResult{}, errHSIConfigNotFound
		}
		return storage.CASResult{Delete: true}, nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errHSIConfigNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": errHSIConfigNotFound.Error()})
		case errors.Is(err, storage.ErrCASConflict):
			c.JSON(http.StatusConflict, gin.H{"error": "Concurrent update conflict, please retry"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete HSI config"})
		}
		return
	}

	logrus.Infof("HSI config deleted for node %s, user: %s", nodeId, userId)
	c.JSON(http.StatusOK, gin.H{"message": "HSI config deleted successfully"})
}

// setDesireStatus CAS-updates the user's HSI config so its desire_status is the
// requested value, preserving every other config field. node watches this
// field and reconciles the PPPoE link. Returns errHSIConfigNotFound when the
// config is absent and storage.ErrCASConflict on retry exhaustion.
func (r *RestServer) setDesireStatus(ctx context.Context, nodeId, userId, username, desire string) error {
	key := fmt.Sprintf("configs/%s/hsi/%s", nodeId, userId)
	return r.etcd.CAS(ctx, key, func(current []byte) (storage.CASResult, error) {
		if current == nil {
			return storage.CASResult{}, errHSIConfigNotFound
		}

		var cwm HSIConfigWithMetadata
		// Guard against wiping a malformed/legacy value: require a real config.
		if err := json.Unmarshal(current, &cwm); err != nil {
			return storage.CASResult{}, fmt.Errorf("parse HSI config: %w", err)
		}
		if cwm.Config.UserID == "" {
			return storage.CASResult{}, errors.New("parse HSI config: stored value has no user_id")
		}

		cwm.Config.DesireStatus = desire
		cwm.Metadata.ResourceVersion = nextResourceVersion(current)
		cwm.Metadata.UpdatedBy = username
		cwm.Metadata.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

		b, err := json.Marshal(cwm)
		if err != nil {
			return storage.CASResult{}, err
		}
		return storage.CASResult{Value: b}, nil
	})
}

// respondDesireStatusError maps a setDesireStatus failure to an HTTP response.
func respondDesireStatusError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errHSIConfigNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": errHSIConfigNotFound.Error()})
	case errors.Is(err, storage.ErrCASConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "Concurrent update conflict, please retry"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update PPPoE desire status"})
	}
}

// DialPPPoE requests a PPPoE connection by setting the config's desire_status
// to "connect"; the node reconciles the actual link.
// @Summary      Dial PPPoE
// @Description  Set desire_status=connect on the HSI config so the node establishes the PPPoE link
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

	if err := validation.ValidateNodeID(req.NodeID); err != nil {
		respondValidationError(c, err)
		return
	}
	if err := validation.ValidateUserID(req.UserID); err != nil {
		respondValidationError(c, err)
		return
	}

	ctx := c.Request.Context()

	subscriberCount := r.GetSubscriberCount(ctx, req.NodeID)
	if subscriberCount < 0 {
		logrus.Infof("No valid subscriber count found for node %s, proceeding without filtering", req.NodeID)
	}
	if err := validation.CheckSubscriberCount(req.UserID, subscriberCount); err != nil {
		respondValidationError(c, err)
		return
	}

	username, err := r.getUserFromToken(c.GetHeader("Authorization"))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Failed to get user from token"})
		return
	}

	if err := r.setDesireStatus(ctx, req.NodeID, req.UserID, username, desireStatusConnect); err != nil {
		respondDesireStatusError(c, err)
		return
	}

	logrus.Infof("PPPoE desire_status set to connect for node %s user %s by %s", req.NodeID, req.UserID, username)
	c.JSON(http.StatusOK, gin.H{"message": "PPPoE dial request accepted"})
}

// HangupPPPoE requests a PPPoE disconnect by setting the config's desire_status
// to "disconnect"; the node reconciles the actual link.
// @Summary      Hangup PPPoE
// @Description  Set desire_status=disconnect on the HSI config so the node tears down the PPPoE link
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

	if err := validation.ValidateNodeID(req.NodeID); err != nil {
		respondValidationError(c, err)
		return
	}
	if err := validation.ValidateUserID(req.UserID); err != nil {
		respondValidationError(c, err)
		return
	}

	ctx := c.Request.Context()

	subscriberCount := r.GetSubscriberCount(ctx, req.NodeID)
	if subscriberCount < 0 {
		logrus.Infof("No valid subscriber count found for node %s, proceeding without filtering", req.NodeID)
	}
	if err := validation.CheckSubscriberCount(req.UserID, subscriberCount); err != nil {
		respondValidationError(c, err)
		return
	}

	username, err := r.getUserFromToken(c.GetHeader("Authorization"))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Failed to get user from token"})
		return
	}

	if err := r.setDesireStatus(ctx, req.NodeID, req.UserID, username, desireStatusDisconnect); err != nil {
		respondDesireStatusError(c, err)
		return
	}

	logrus.Infof("PPPoE desire_status set to disconnect for node %s user %s by %s", req.NodeID, req.UserID, username)
	c.JSON(http.StatusOK, gin.H{"message": "PPPoE hangup request accepted"})
}

// GetDhcpLeaseCount returns current DHCP lease count for a user on a node
// @Summary      Get DHCP Lease Count
// @Description  Get the current number of allocated DHCP IPs for a specific user on a node
// @Tags         DHCP
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId  path      string  true  "Node ID"
// @Param        userId  path      string  true  "User ID"
// @Success      200     {object}  map[string]interface{}
// @Failure      400     {object}  ErrorResponse
// @Failure      404     {object}  ErrorResponse
// @Failure      500     {object}  ErrorResponse
// @Router       /config/{nodeId}/dhcp/lease/{userId} [get]
func (r *RestServer) GetDhcpLeaseCount(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")

	if r.nodeMonitorMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Node monitor not available"})
		return
	}

	ctx, cancel := boundedRequestCtx(c)
	defer cancel()

	result, found, err := r.nodeMonitorMgr.GetNodeDhcpLease(ctx, nodeId, userId)
	if err != nil {
		logrus.WithError(err).Error("Failed to get DHCP lease info")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get DHCP lease info"})
		return
	}

	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found or not connected"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"cur_lease_count": result.CurLeaseCount,
		"max_lease_count": result.MaxLeaseCount,
		"inuse_ips":       result.InuseIps,
		"status":          result.Status,
	})
}

// GetArpTable returns ARP table for a user on a node
// @Summary      Get ARP Table
// @Description  Get the current ARP table entries for a specific user on a node
// @Tags         ARP
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId  path      string  true  "Node ID"
// @Param        userId  path      string  true  "User ID"
// @Success      200     {object}  ArpTableResult
// @Failure      400     {object}  ErrorResponse
// @Failure      404     {object}  ErrorResponse
// @Failure      500     {object}  ErrorResponse
// @Router       /config/{nodeId}/arp/{userId} [get]
func (r *RestServer) GetArpTable(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")

	logrus.Infof("GetArpTable called for nodeId=%s, userId=%s", nodeId, userId)

	if r.nodeMonitorMgr == nil {
		logrus.Errorf("Node monitor manager is nil")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Node monitor not available"})
		return
	}

	ctx, cancel := boundedRequestCtx(c)
	defer cancel()

	result, found, err := r.nodeMonitorMgr.GetNodeArpTable(ctx, nodeId, userId)
	if err != nil {
		logrus.Errorf("GetNodeArpTable error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get ARP table"})
		return
	}

	if !found {
		logrus.Warnf("GetNodeArpTable: Node not found or not connected for nodeId=%s", nodeId)
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found or not connected"})
		return
	}

	logrus.Infof("GetArpTable success: returned %d entries", len(result.Entries))
	c.JSON(http.StatusOK, result)
}

// GetDnsCache returns DNS cache for a user on a node
// @Summary      Get DNS Cache
// @Description  Get the current DNS cache entries for a specific user on a node
// @Tags         DNS
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId  path      string  true  "Node ID"
// @Param        userId  path      string  true  "User ID"
// @Success      200     {object}  DnsCacheResult
// @Failure      400     {object}  ErrorResponse
// @Failure      404     {object}  ErrorResponse
// @Failure      500     {object}  ErrorResponse
// @Router       /config/{nodeId}/dns-cache/{userId} [get]
func (r *RestServer) GetDnsCache(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")

	logrus.Infof("GetDnsCache called for nodeId=%s, userId=%s", nodeId, userId)

	if r.nodeMonitorMgr == nil {
		logrus.Errorf("Node monitor manager is nil")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Node monitor not available"})
		return
	}

	ctx, cancel := boundedRequestCtx(c)
	defer cancel()

	result, found, err := r.nodeMonitorMgr.GetNodeDnsCache(ctx, nodeId, userId)
	if err != nil {
		logrus.Errorf("GetNodeDnsCache error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get DNS cache"})
		return
	}

	if !found {
		logrus.Warnf("GetNodeDnsCache: Node not found or not connected for nodeId=%s", nodeId)
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found or not connected"})
		return
	}

	logrus.Infof("GetDnsCache success: returned %d entries", len(result.Entries))
	c.JSON(http.StatusOK, result)
}

// GetPPPoEInfo returns PPPoE session information for a user on a node
// @Summary      Get PPPoE Info
// @Description  Get the current PPPoE session info for a specific user on a node
// @Tags         PPPoE
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId  path      string  true  "Node ID"
// @Param        userId  path      string  true  "User ID"
// @Success      200     {object}  PPPoEInfo
// @Failure      400     {object}  ErrorResponse
// @Failure      404     {object}  ErrorResponse
// @Failure      500     {object}  ErrorResponse
// @Router       /config/{nodeId}/pppoe/{userId} [get]
func (r *RestServer) GetPPPoEInfo(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")

	logrus.Infof("GetPPPoEInfo called for nodeId=%s, userId=%s", nodeId, userId)

	if r.nodeMonitorMgr == nil {
		logrus.Errorf("Node monitor manager is nil")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Node monitor not available"})
		return
	}

	ctx, cancel := boundedRequestCtx(c)
	defer cancel()

	result, found, err := r.nodeMonitorMgr.GetNodePPPoEInfo(ctx, nodeId, userId)
	if err != nil {
		logrus.Errorf("GetNodePPPoEInfo error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get PPPoE info"})
		return
	}

	if !found {
		logrus.Warnf("GetNodePPPoEInfo: Node not found or not connected for nodeId=%s", nodeId)
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found or not connected"})
		return
	}
	if result == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "PPPoE session not found for user"})
		return
	}

	logrus.Infof("GetPPPoEInfo success")
	c.JSON(http.StatusOK, result)
}

// GetDhcpConfig returns DHCP server configuration for a user on a node
// @Summary      Get DHCP Config
// @Description  Get the current DHCP server configuration for a specific user on a node
// @Tags         DHCP
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId  path      string  true  "Node ID"
// @Param        userId  path      string  true  "User ID"
// @Success      200     {object}  DhcpConfig
// @Failure      400     {object}  ErrorResponse
// @Failure      404     {object}  ErrorResponse
// @Failure      500     {object}  ErrorResponse
// @Router       /config/{nodeId}/dhcp/{userId} [get]
func (r *RestServer) GetDhcpConfig(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")

	logrus.Infof("GetDhcpConfig called for nodeId=%s, userId=%s", nodeId, userId)

	if r.nodeMonitorMgr == nil {
		logrus.Errorf("Node monitor manager is nil")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Node monitor not available"})
		return
	}

	ctx, cancel := boundedRequestCtx(c)
	defer cancel()

	result, found, err := r.nodeMonitorMgr.GetNodeDhcpConfig(ctx, nodeId, userId)
	if err != nil {
		logrus.Errorf("GetNodeDhcpConfig error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get DHCP config"})
		return
	}

	if !found {
		logrus.Warnf("GetNodeDhcpConfig: Node not found or not connected for nodeId=%s", nodeId)
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found or not connected"})
		return
	}
	if result == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "DHCP config not found for user"})
		return
	}

	logrus.Infof("GetDhcpConfig success")
	c.JSON(http.StatusOK, result)
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

	if err := validation.ValidateSubscriberCount(req.SubscriberCount); err != nil {
		respondValidationError(c, err)
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

	key := fmt.Sprintf("user_counts/%s/", nodeId)
	err = r.etcd.CAS(ctx, key, func(current []byte) (storage.CASResult, error) {
		countData := SubscriberCountData{}
		countData.SubscriberCount = fmt.Sprintf("%d", req.SubscriberCount)
		countData.Metadata.Node = nodeId
		countData.Metadata.ResourceVersion = nextResourceVersion(current)
		countData.Metadata.UpdatedBy = username
		countData.Metadata.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

		countJSON, err := json.Marshal(countData)
		if err != nil {
			return storage.CASResult{}, err
		}
		return storage.CASResult{Value: countJSON}, nil
	})
	if err != nil {
		logrus.WithError(err).Errorf("Failed to update subscriber count for node %s", nodeId)
		if errors.Is(err, storage.ErrCASConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "Concurrent update conflict, please retry"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update subscriber count"})
		}
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

// FailedEventsResponse represents the response for node events
type FailedEventsResponse struct {
	Events []db.NodeEventRow `json:"events"`
}

// requireDB returns the current database, or writes a 503 when none is ready.
func (r *RestServer) requireDB(c *gin.Context) *db.DB {
	database := r.Database()
	if database == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Database not configured"})
		return nil
	}
	return database
}

// GetFailedEvents returns node events for a specific node (from PostgreSQL).
// @Summary      Get node events for a node
// @Description  Get a list of node events (config-apply results, runtime errors) for a specific node
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
	database := r.requireDB(c)
	if database == nil {
		return
	}

	events, err := database.ListNodeEvents(c.Request.Context(), nodeId, c.Query("event_type"), 0)
	if err != nil {
		logrus.WithError(err).Error("Failed to list node events")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get node events"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}

// GetAllFailedEvents returns node events across all nodes (from PostgreSQL).
// @Summary      Get all node events
// @Description  Get a list of all node events across all nodes. Supports optional event_type filter.
// @Tags         Failed Events
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        event_type  query     string  false  "Filter by event type (e.g., RUNTIME_ERROR)"
// @Success      200         {object}  FailedEventsResponse
// @Failure      500         {object}  ErrorResponse
// @Router       /failed-events [get]
func (r *RestServer) GetAllFailedEvents(c *gin.Context) {
	database := r.requireDB(c)
	if database == nil {
		return
	}
	events, err := database.ListNodeEvents(c.Request.Context(), "", c.Query("event_type"), 0)
	if err != nil {
		logrus.WithError(err).Error("Failed to list node events")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get node events"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}

// DeleteFailedEventsRequest represents the request body for deleting node events
type DeleteFailedEventsRequest struct {
	IDs []int64 `json:"ids" binding:"required"`
}

// DeleteFailedEvents deletes node events by id (from PostgreSQL).
// @Summary      Delete node events
// @Description  Delete one or more node events by their numeric ids
// @Tags         Failed Events
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      DeleteFailedEventsRequest  true  "Event ids to delete"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  ErrorResponse
// @Failure      500      {object}  ErrorResponse
// @Router       /failed-events [delete]
func (r *RestServer) DeleteFailedEvents(c *gin.Context) {
	var req DeleteFailedEventsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No ids provided"})
		return
	}
	database := r.requireDB(c)
	if database == nil {
		return
	}

	deleted, err := database.DeleteNodeEvents(c.Request.Context(), req.IDs)
	if err != nil {
		logrus.WithError(err).Error("Failed to delete node events")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete node events"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}

// GetPPPoEStatus returns the latest Kafka-fed PPPoE state for a user (from
// PostgreSQL), surviving node/controller restarts. This is the recorded actual
// state, distinct from the live node-scrape in GetPPPoEInfo.
// @Summary      Get recorded PPPoE status
// @Description  Get the latest PPPoE phase recorded from node events
// @Tags         PPPoE
// @Produce      json
// @Security     BearerAuth
// @Param        nodeId  path      string  true  "Node ID"
// @Param        userId  path      string  true  "User ID"
// @Success      200     {object}  db.PPPoEStatusRow
// @Failure      400     {object}  ErrorResponse
// @Failure      404     {object}  ErrorResponse
// @Failure      500     {object}  ErrorResponse
// @Router       /pppoe/status/{nodeId}/{userId} [get]
func (r *RestServer) GetPPPoEStatus(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")
	if nodeId == "" || userId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID and User ID are required"})
		return
	}
	database := r.requireDB(c)
	if database == nil {
		return
	}

	status, ok, err := database.GetPPPoEStatus(c.Request.Context(), nodeId, userId)
	if err != nil {
		logrus.WithError(err).Error("Failed to get PPPoE status")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get PPPoE status"})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "No PPPoE status recorded for this user"})
		return
	}
	c.JSON(http.StatusOK, status)
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
	key := fmt.Sprintf("configs/%s/dns/%s", nodeId, userId)
	resp, err := r.etcd.Client().Get(ctx, key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get DNS records"})
		return
	}

	records := []DnsRecord{}
	if len(resp.Kvs) > 0 {
		if err := json.Unmarshal(resp.Kvs[0].Value, &records); err != nil {
			logrus.WithError(err).Errorf("Failed to parse DNS records")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse DNS records"})
			return
		}
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
	key := fmt.Sprintf("configs/%s/dns/%s", nodeId, userId)
	resp, err := r.etcd.Client().Get(ctx, key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get DNS records"})
		return
	}

	if len(resp.Kvs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "DNS record not found"})
		return
	}

	var records []DnsRecord
	if err := json.Unmarshal(resp.Kvs[0].Value, &records); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse DNS records"})
		return
	}

	for _, r := range records {
		if r.Domain == domain {
			c.JSON(http.StatusOK, r)
			return
		}
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "DNS record not found"})
}

// AddOrUpdateDnsRecord creates or updates a static DNS record
func (r *RestServer) AddOrUpdateDnsRecord(c *gin.Context) {
	nodeId := c.Param("nodeId")
	userId := c.Param("userId")
	if nodeId == "" || userId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node ID and User ID are required"})
		return
	}

	var newRecord DnsRecord
	if err := c.ShouldBindJSON(&newRecord); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	if err := validation.ValidateDnsRecord(newRecord.Domain, newRecord.IP, newRecord.TTL); err != nil {
		respondValidationError(c, err)
		return
	}

	ctx := c.Request.Context()
	key := fmt.Sprintf("configs/%s/dns/%s", nodeId, userId)

	// CAS the whole DNS record array: read current, add/update the entry,
	// enforce the 64-record cap, write back atomically.
	isUpdate := false
	err := r.etcd.CAS(ctx, key, func(current []byte) (storage.CASResult, error) {
		var records []DnsRecord
		if current != nil {
			if err := json.Unmarshal(current, &records); err != nil {
				return storage.CASResult{}, err
			}
		}

		isUpdate = false
		for i, rec := range records {
			if rec.Domain == newRecord.Domain {
				records[i] = newRecord
				isUpdate = true
				break
			}
		}
		if !isUpdate {
			if len(records) >= 64 {
				return storage.CASResult{}, errDNSRecordLimit
			}
			records = append(records, newRecord)
		}

		data, err := json.Marshal(records)
		if err != nil {
			return storage.CASResult{}, err
		}
		return storage.CASResult{Value: data}, nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errDNSRecordLimit):
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": errDNSRecordLimit.Error()})
		case errors.Is(err, storage.ErrCASConflict):
			c.JSON(http.StatusConflict, gin.H{"error": "Concurrent update conflict, please retry"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save DNS records"})
		}
		return
	}

	if isUpdate {
		logrus.Infof("DNS record updated for node %s user %s domain %s", nodeId, userId, newRecord.Domain)
		c.JSON(http.StatusOK, gin.H{"message": "DNS record updated successfully", "action": "updated"})
	} else {
		logrus.Infof("DNS record added for node %s user %s domain %s", nodeId, userId, newRecord.Domain)
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
	key := fmt.Sprintf("configs/%s/dns/%s", nodeId, userId)

	// CAS the DNS record array: remove the domain, then delete the key when no
	// records remain or write back the trimmed array, atomically.
	err := r.etcd.CAS(ctx, key, func(current []byte) (storage.CASResult, error) {
		if current == nil {
			return storage.CASResult{}, errDNSRecordNotFound
		}

		var records []DnsRecord
		if err := json.Unmarshal(current, &records); err != nil {
			return storage.CASResult{}, err
		}

		found := false
		newRecords := records[:0]
		for _, rec := range records {
			if rec.Domain == domain {
				found = true
			} else {
				newRecords = append(newRecords, rec)
			}
		}
		if !found {
			return storage.CASResult{}, errDNSRecordNotFound
		}

		if len(newRecords) == 0 {
			return storage.CASResult{Delete: true}, nil
		}
		data, err := json.Marshal(newRecords)
		if err != nil {
			return storage.CASResult{}, err
		}
		return storage.CASResult{Value: data}, nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errDNSRecordNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": errDNSRecordNotFound.Error()})
		case errors.Is(err, storage.ErrCASConflict):
			c.JSON(http.StatusConflict, gin.H{"error": "Concurrent update conflict, please retry"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save DNS records"})
		}
		return
	}

	logrus.Infof("DNS record deleted for node %s user %s domain %s", nodeId, userId, domain)
	c.JSON(http.StatusOK, gin.H{"message": "DNS record deleted successfully"})
}

func (r *RestServer) newRouter() *gin.Engine {
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
	api.Use(validatePathIDs())
	{
		// Health check endpoint (no authentication required)
		api.GET("/health", r.EtcdHealthCheck)

		api.POST("/login", r.Login)
		api.POST("/logout", r.AuthMiddlewareWithBlacklist(), r.Logout)
		api.POST("/verify-password", r.AuthMiddlewareWithBlacklist(), r.VerifyPassword)
		api.GET("/nodes", r.AuthMiddlewareWithBlacklist(), r.ListNodes)
		api.DELETE("/nodes/inactive", r.AuthMiddlewareWithBlacklist(), r.ClearInactiveNodes)
		api.DELETE("/nodes/:nodeId", r.AuthMiddlewareWithBlacklist(), r.UnregisterNode)
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
		api.GET("/pppoe/status/:nodeId/:userId", r.AuthMiddlewareWithBlacklist(), r.GetPPPoEStatus)
		api.GET("/config/:nodeId/dhcp/lease/:userId", r.AuthMiddlewareWithBlacklist(), r.GetDhcpLeaseCount)
		api.GET("/config/:nodeId/arp/:userId", r.AuthMiddlewareWithBlacklist(), r.GetArpTable)
		api.GET("/config/:nodeId/dns-cache/:userId", r.AuthMiddlewareWithBlacklist(), r.GetDnsCache)
		api.GET("/config/:nodeId/pppoe/:userId", r.AuthMiddlewareWithBlacklist(), r.GetPPPoEInfo)
		api.GET("/config/:nodeId/dhcp/:userId", r.AuthMiddlewareWithBlacklist(), r.GetDhcpConfig)

		// Static DNS record management
		api.GET("/config/:nodeId/dns/:userId", r.AuthMiddlewareWithBlacklist(), r.GetDnsRecords)
		api.GET("/config/:nodeId/dns/:userId/:domain", r.AuthMiddlewareWithBlacklist(), r.GetDnsRecord)
		api.POST("/config/:nodeId/dns/:userId", r.AuthMiddlewareWithBlacklist(), r.AddOrUpdateDnsRecord)
		api.DELETE("/config/:nodeId/dns/:userId/:domain", r.AuthMiddlewareWithBlacklist(), r.DeleteDnsRecord)

		// Failed events endpoints
		api.GET("/failed-events", r.AuthMiddlewareWithBlacklist(), r.GetAllFailedEvents)
		api.DELETE("/failed-events", r.AuthMiddlewareWithBlacklist(), r.DeleteFailedEvents)
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
	// Unmatched API routes must remain HTTP 404; frontend paths fall back to the
	// index page so the SPA router can handle them.
	router.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
			return
		}
		c.File("./web/build/index.html")
	})

	return router
}

// NewHardenedTLSServer returns an HTTP server for handler (nil means
// http.DefaultServeMux) with TLS capped below at 1.2 — Go's server-side
// default still accepts TLS 1.0 handshakes.
func NewHardenedTLSServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:      addr,
		Handler:   handler,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

func (r *RestServer) StartRestServer(addr string) error {
	gin.SetMode(gin.ReleaseMode)
	router := r.newRouter()

	// ---- Start HTTPS server ----
	certFile := os.Getenv("CERT_FILE")
	if certFile == "" {
		certFile = "./certs/server.crt"
	}
	keyFile := os.Getenv("KEY_FILE")
	if keyFile == "" {
		keyFile = "./certs/server.key"
	}
	return NewHardenedTLSServer(addr, router).ListenAndServeTLS(certFile, keyFile)
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
