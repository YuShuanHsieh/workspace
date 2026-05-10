# Background
There are two services handle the permission access control:
- access management: provide the permission management functions. so the permission context like the user's attributes come from the service.
- permission checking service: it mainly provides the permission checking function. It exposes the permission function using HTTP post method with json payload: {"userId", "objectId"}

For application developers, they first need to setup the app's permissions via `access management` and then use both `access management` and `permission checking service` to check user's permission runtime before the application service processes user's requests.

# Purpose
We provide a access control management that allows application can plugin their permissions and end-user can setup the permissions to control who can access the application's resource. In this PRD, we want to provide a permission validation flow before user's requests be processed by applications. The permission validation is first get the required permission context from access management APIs/HTTP headers and then sends the context to permission checking service. The permission checking service is designed to handle huge requests in a short-time.


# Idea 
- the deployment env is K8s. so the sidecar pattern may be good way to integrate the permission validation flow before application service processes requests.

# SLO
- The peak RPS maybe 5000. and the duration for the permission validation flow should be < 5ms.

# Constraints
- We are platform team that provides access management and permission checking service. The applications are developed by another teams.
