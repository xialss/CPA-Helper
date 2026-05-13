class AppError(Exception):
    code = "app_error"
    status_code = 400

    def __init__(self, message: str) -> None:
        self.message = message
        super().__init__(message)


class AuthenticationError(AppError):
    code = "authentication_failed"
    status_code = 401


class ForbiddenError(AppError):
    code = "forbidden"
    status_code = 403


class NotFoundError(AppError):
    code = "not_found"
    status_code = 404


class ConflictError(AppError):
    code = "conflict"
    status_code = 409


class ValidationAppError(AppError):
    code = "validation_error"
    status_code = 422

