package handler

import (
	"bus/app/internal/models"
	storageModels "bus/app/internal/models"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

type CreateUserInput struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Role     string `json:"role" binding:"required,oneof=admin user"`
}

type ChangePasswordInput struct {
	Username    string `json:"username" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// @Summary Create user
// @Tags Users
// @Description Creating a new user
// @ID createUser
// @Accept json
// @Produce json
// @Param input body CreateUserInput true "User"
// @Success 200 {object} map[string]string
// @Failure default {object} errorResponse
// @Security BasicAuth
// @Router /api/users/createUser [post]
func (h *Handler) createUser(c *gin.Context) {

	role := userRoleOutOfContex(c)
	if !role.IsAdmin() {
		newErrorResponse(c, http.StatusForbidden, "handler.createUser", errors.New("only admin can create users"))
		return
	}

	const op = "handler.createUser"
	var input CreateUserInput
	var err error
	if err = c.ShouldBindJSON(&input); err != nil {
		newErrorResponse(c, http.StatusBadRequest, op+".ShouldBindJSON", err)
		return
	}

	userID, err := h.store.CreateUser(h.ctx, input.Username, input.Password, storageModels.Role(input.Role))
	if err != nil {
		newErrorResponse(c, http.StatusInternalServerError, op+".CreateUser", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"result": "ok",
		"userID": userID,
	})
}

// @Summary Change user password
// @Tags Users
// @Description Changing user password
// @ID changeUserPassword
// @Accept json
// @Produce json
// @Param input body ChangePasswordInput true "Input"
// @Success 200 {object} map[string]string
// @Failure default {object} errorResponse
// @Security BasicAuth
// @Router /api/users/changeUserPassword [post]
func (h *Handler) changeUserPassword(c *gin.Context) {

	role := userRoleOutOfContex(c)
	if !role.IsAdmin() {
		newErrorResponse(c, http.StatusForbidden, "handler.createUser", errors.New("only admin can change user password"))
		return
	}

	const op = "handler.changeUserPassword"
	var msg ChangePasswordInput
	var err error
	if err = c.ShouldBindJSON(&msg); err != nil {
		newErrorResponse(c, http.StatusBadRequest, op+".ShouldBindJSON", err)
		return
	}

	err = h.store.ChangeUserPassword(h.ctx, msg.Username, msg.NewPassword)
	if err != nil {
		newErrorResponse(c, http.StatusInternalServerError, op+".ChangeUserPassword", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"result": "ok",
	})

}

func userRoleOutOfContex(c *gin.Context) *models.Role {

	value, exists := c.Get("userRole")
	if !exists {
		return nil
	}

	role, ok := value.(*models.Role)
	if !ok {
		return nil
	}

	return role
}
