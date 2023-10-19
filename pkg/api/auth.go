package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/crewjam/saml/samlsp"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/ucarion/saml"
	"golang.org/x/crypto/bcrypt"
)

//go:embed manifests/samlconnecter.html
var samlConnecterHTMLTemplate string

var jwtSigningKey string = os.Getenv("JWT_SIGNING_KEY")

// TODO move user management into the database
var adminUsername string = os.Getenv("ADMIN_USERNAME")
var adminPassword string = os.Getenv("ADMIN_PASSWORD")

func unauthorized(c *gin.Context, reason string) {
	c.JSON(http.StatusUnauthorized, response(http.StatusUnauthorized, reason, []string{}))
	c.Abort()
}

func userToken(c *gin.Context) (string, error) {
	if c.Request.Header["Token"] == nil && c.Query("token") == "" {
		return "", fmt.Errorf("token not in header")
	}

	var userProvidedJWT string
	if tokenHeader := c.Request.Header["Token"]; tokenHeader != nil {
		userProvidedJWT = tokenHeader[0]
	} else {
		userProvidedJWT = c.Query("token")
	}
	return userProvidedJWT, nil
}

func validateJwt(c *gin.Context) {

	userProvidedJWT, err := userToken(c)
	if err != nil {
		unauthorized(c, err.Error())
	}

	token, err := jwt.Parse(userProvidedJWT, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("there was an error in parsing")
		}
		return []byte(jwtSigningKey), nil
	})

	if err != nil {
		unauthorized(c, err.Error())
		return
	}

	if token == nil {
		unauthorized(c, "invalid token")
		return
	}
}

func (h APIHandler) login(c *gin.Context) {
	jsonData := struct {
		Username string `json:"user"`
		Password string `json:"password"`
	}{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		unauthorized(c, fmt.Sprintf("Error paring data: %s", err.Error()))
		return
	}

	if jsonData.Username != adminUsername {
		unauthorized(c, "Username or password incorrect")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(adminPassword), []byte(jsonData.Password)) != nil {
		unauthorized(c, "Username or password incorrect")
		return
	}

	token, err := generateJWT(jsonData.Username)
	if err != nil {
		unauthorized(c, fmt.Sprintf("Error issuing JWT: %s", err.Error()))
		return
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", []string{token}))
}

func generateJWT(username string) (string, error) {
	token := jwt.New(jwt.SigningMethodHS256)
	claims := token.Claims.(jwt.MapClaims)

	claims["authorized"] = true
	claims["username"] = username
	claims["exp"] = time.Now().Add(time.Hour * 12).Unix()

	tokenString, err := token.SignedString([]byte(jwtSigningKey))

	if err != nil {
		return "", fmt.Errorf("something went wrong: %s", err.Error())
	}
	return tokenString, nil
}

func (h APIHandler) defaultConnectMethod(c *gin.Context) {
	if h.ssoConfig != nil {
		c.JSON(http.StatusOK, response(http.StatusOK, "", []string{"sso"}))
		return
	} else {
		c.JSON(http.StatusOK, response(http.StatusOK, "", []string{"basic"}))
		return
	}

}

func (h APIHandler) ssoRedirecter(c *gin.Context) {
	if h.ssoConfig == nil {
		c.AbortWithError(http.StatusNotAcceptable, fmt.Errorf("no SAML configuration found on server"))
		return
	}
	c.Redirect(http.StatusMovedPermanently, h.ssoConfig.URL)
}

func (h APIHandler) samlConnecter(c *gin.Context) {
	if h.ssoConfig == nil {
		c.AbortWithError(http.StatusNotAcceptable, fmt.Errorf("no SAML configuration found on server"))
		return
	}
	if h.ssoConfig.saml == nil {
		c.AbortWithError(http.StatusNotAcceptable, fmt.Errorf("no SAML configuration found on server"))
		return
	}

	err := c.Request.ParseForm()
	if err != nil {
		c.AbortWithError(http.StatusNotAcceptable, err)
	}

	data := c.Request.Form
	samlResponseData := data.Get("SAMLResponse")

	samlResponse, err := saml.Verify(
		samlResponseData,
		h.ssoConfig.saml.issuer,
		h.ssoConfig.saml.crt,
		h.ssoConfig.saml.recipient,
		time.Now(),
	)

	if err != nil {
		c.AbortWithError(http.StatusNotAcceptable, err)
		return
	}
	username := samlResponse.Assertion.Subject.NameID.Value
	if err != nil {
		c.AbortWithError(http.StatusNotAcceptable, fmt.Errorf("username not found"))
		return
	}

	jwtToken, err := generateJWT(username)
	if err != nil {
		c.AbortWithError(http.StatusNotAcceptable, err)
		return
	}

	buf := bytes.NewBuffer([]byte{})
	tmpl, _ := template.New("").Parse(string(samlConnecterHTMLTemplate))
	err = tmpl.Execute(buf, struct {
		Token string `json:"token"`
	}{
		Token: jwtToken,
	})
	if err != nil {
		c.AbortWithError(http.StatusNotAcceptable, err)
		return
	}

	fmt.Fprintln(c.Writer, buf.String())
}

func fetchIDPCertificate(metadataURL string) (*x509.Certificate, error) {
	url, err := url.Parse(metadataURL)
	if err != nil {
		panic(err)
	}

	httpClient := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	entityDescriptor, err := samlsp.FetchMetadata(context.Background(), &httpClient, *url)
	if err != nil {
		panic(err)
	}

	crt := entityDescriptor.IDPSSODescriptors[0].KeyDescriptors[0].KeyInfo.X509Data.X509Certificates[0].Data
	b, err := base64.StdEncoding.DecodeString(crt)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(b)

}

// TODO generateLogJWT is a temporary function to provide access to some task endpoints. Hopefully this
// can be removed for a better method soon.
//
// Grant 30 days of access per issued token.
func generateTaskJWT(resourceUUID, tenant, clientName, generation string) (string, error) {
	token := jwt.New(jwt.SigningMethodHS256)
	claims := token.Claims.(jwt.MapClaims)

	claims["exp"] = time.Now().Add(time.Hour * 720).Unix()
	claims["authorized"] = true
	claims["resourceUUID"] = resourceUUID
	claims["generation"] = generation

	tokenString, err := token.SignedString([]byte(jwtSigningKey))

	if err != nil {
		return "", fmt.Errorf("something went wrong: %s", err.Error())
	}
	return tokenString, nil
}

// Check that the  taskJWT is correctly formatted with all the claim fields defined
func validateTaskJWT(c *gin.Context) {

	if c.Request.Header["Token"] == nil {
		unauthorized(c, "token not in header")
		return
	}

	token, err := jwt.Parse(c.Request.Header["Token"][0], func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("there was an error in parsing")
		}

		// Validate that the claims have all the required fields
		claims := t.Claims.(jwt.MapClaims)
		if _, ok := claims["resourceUUID"]; !ok {
			return nil, fmt.Errorf("invalid claim")
		}
		if _, ok := claims["generation"]; !ok {
			return nil, fmt.Errorf("invalid claim")
		}

		return []byte(jwtSigningKey), nil
	})

	if err != nil {
		unauthorized(c, err.Error())
		return
	}

	if token == nil {
		unauthorized(c, "invalid token")
		return
	}
}

func taskJWT(tokenHeader string) (*jwt.Token, error) {
	return jwt.Parse(tokenHeader, func(t *jwt.Token) (interface{}, error) {
		return []byte(jwtSigningKey), nil
	})
}

// Return the claims from the taskJWT in a easy to consume map ie. no interface
func taskJWTClaims(token *jwt.Token) map[string]string {
	claimsMap := map[string]string{}

	claims := token.Claims.(jwt.MapClaims)
	for key, value := range claims {
		switch c := value.(type) {
		case string:
			claimsMap[key] = c
		case time.Time:
			claimsMap[key] = c.String()
		default:
			continue
		}
	}

	return claimsMap
}

func (h APIHandler) corsOK(c *gin.Context) {
	if c.Request.Method == http.MethodOptions {
		c.Status(http.StatusOK)
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE")
		c.Header("Access-Control-Allow-Headers", "*")
		c.Header("Access-Control-Max-Age", "86400")
		c.AbortWithStatus(http.StatusOK)
	} else {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Headers", "*")
		c.Header("Access-Control-Max-Age", "86400")
		c.Header("Access-Control-Allow-Methods", c.Request.Method)
		c.Next()
	}
}
