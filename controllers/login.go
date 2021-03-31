package controllers

import (
	"github.com/crawlab-team/crawlab-core/errors"
	"github.com/gin-gonic/gin"
)

func Login(c *gin.Context) {
	panic(errors.ErrorControllerNotImplemented)
}

func Logout(c *gin.Context) {
	panic(errors.ErrorControllerNotImplemented)
}

var LoginController = NewPostActionControllerDelegate(ControllerIdLogin, []PostAction{
	{"/login", Login},
	{"/logout", Logout},
})
