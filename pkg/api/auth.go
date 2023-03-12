package api

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"golang.org/x/crypto/bcrypt"
)

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
