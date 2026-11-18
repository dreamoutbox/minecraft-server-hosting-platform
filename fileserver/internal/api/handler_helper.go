package api

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
)

func parseUintParam(c *gin.Context, name string) (uint, error) {
	val, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || val == 0 {
		return 0, errors.New("invalid uint parameter")
	}
	return uint(val), nil
}
