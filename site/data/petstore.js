// Generated from testdata/specs/petstore3.json by PlugIt's own parser and token
// estimator (internal/openapi, internal/tokens): the real 19 tools.
export const PETSTORE = {
 "spec": "Swagger Petstore - OpenAPI 3.0",
 "version": "1.0.27",
 "total": 2287,
 "tools": [
  {
   "key": "DELETE /pet/{petId}",
   "name": "deletePet",
   "method": "DELETE",
   "path": "/pet/{petId}",
   "tag": "pet",
   "desc": "Deletes a pet.",
   "safety": "destructive",
   "tokens": 94
  },
  {
   "key": "DELETE /store/order/{orderId}",
   "name": "deleteOrder",
   "method": "DELETE",
   "path": "/store/order/{orderId}",
   "tag": "store",
   "desc": "Delete purchase order by identifier.",
   "safety": "destructive",
   "tokens": 92
  },
  {
   "key": "DELETE /user/{username}",
   "name": "deleteUser",
   "method": "DELETE",
   "path": "/user/{username}",
   "tag": "user",
   "desc": "Delete user resource.",
   "safety": "destructive",
   "tokens": 80
  },
  {
   "key": "GET /pet/findByStatus",
   "name": "findPetsByStatus",
   "method": "GET",
   "path": "/pet/findByStatus",
   "tag": "pet",
   "desc": "Finds Pets by status.",
   "safety": "read-only",
   "tokens": 103
  },
  {
   "key": "GET /pet/findByTags",
   "name": "findPetsByTags",
   "method": "GET",
   "path": "/pet/findByTags",
   "tag": "pet",
   "desc": "Finds Pets by tags.",
   "safety": "read-only",
   "tokens": 86
  },
  {
   "key": "GET /pet/{petId}",
   "name": "getPetById",
   "method": "GET",
   "path": "/pet/{petId}",
   "tag": "pet",
   "desc": "Find pet by ID.",
   "safety": "read-only",
   "tokens": 87
  },
  {
   "key": "GET /store/inventory",
   "name": "getInventory",
   "method": "GET",
   "path": "/store/inventory",
   "tag": "store",
   "desc": "Returns pet inventories by status.",
   "safety": "read-only",
   "tokens": 52
  },
  {
   "key": "GET /store/order/{orderId}",
   "name": "getOrderById",
   "method": "GET",
   "path": "/store/order/{orderId}",
   "tag": "store",
   "desc": "Find purchase order by ID.",
   "safety": "read-only",
   "tokens": 92
  },
  {
   "key": "GET /user/login",
   "name": "loginUser",
   "method": "GET",
   "path": "/user/login",
   "tag": "user",
   "desc": "Logs user into the system.",
   "safety": "read-only",
   "tokens": 95
  },
  {
   "key": "GET /user/logout",
   "name": "logoutUser",
   "method": "GET",
   "path": "/user/logout",
   "tag": "user",
   "desc": "Logs out current logged in user session.",
   "safety": "read-only",
   "tokens": 52
  },
  {
   "key": "GET /user/{username}",
   "name": "getUserByName",
   "method": "GET",
   "path": "/user/{username}",
   "tag": "user",
   "desc": "Get user by user name.",
   "safety": "read-only",
   "tokens": 89
  },
  {
   "key": "POST /pet",
   "name": "addPet",
   "method": "POST",
   "path": "/pet",
   "tag": "pet",
   "desc": "Add a new pet to the store.",
   "safety": "write",
   "tokens": 228
  },
  {
   "key": "POST /pet/{petId}",
   "name": "updatePetWithForm",
   "method": "POST",
   "path": "/pet/{petId}",
   "tag": "pet",
   "desc": "Updates a pet in the store with form data.",
   "safety": "write",
   "tokens": 141
  },
  {
   "key": "POST /pet/{petId}/uploadImage",
   "name": "uploadFile",
   "method": "POST",
   "path": "/pet/{petId}/uploadImage",
   "tag": "pet",
   "desc": "Uploads an image.",
   "safety": "write",
   "tokens": 106
  },
  {
   "key": "POST /store/order",
   "name": "placeOrder",
   "method": "POST",
   "path": "/store/order",
   "tag": "store",
   "desc": "Place an order for a pet.",
   "safety": "write",
   "tokens": 160
  },
  {
   "key": "POST /user",
   "name": "createUser",
   "method": "POST",
   "path": "/user",
   "tag": "user",
   "desc": "Create user.",
   "safety": "write",
   "tokens": 146
  },
  {
   "key": "POST /user/createWithList",
   "name": "createUsersWithListInput",
   "method": "POST",
   "path": "/user/createWithList",
   "tag": "user",
   "desc": "Creates list of users with given input array.",
   "safety": "write",
   "tokens": 178
  },
  {
   "key": "PUT /pet",
   "name": "updatePet",
   "method": "PUT",
   "path": "/pet",
   "tag": "pet",
   "desc": "Update an existing pet.",
   "safety": "write",
   "tokens": 226
  },
  {
   "key": "PUT /user/{username}",
   "name": "updateUser",
   "method": "PUT",
   "path": "/user/{username}",
   "tag": "user",
   "desc": "Update user resource.",
   "safety": "write",
   "tokens": 180
  }
 ]
};
