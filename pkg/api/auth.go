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

func validateJwt(c *gin.Context) {

	if c.Request.Header["Token"] == nil {
		unauthorized(c, "token not in header")
		return
	}

	token, err := jwt.Parse(c.Request.Header["Token"][0], func(token *jwt.Token) (interface{}, error) {
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
