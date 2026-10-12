import base64
import hashlib
import secrets
from typing import Tuple
from argon2 import PasswordHasher
from argon2.exceptions import InvalidHash, VerifyMismatchError

# Argon2id パスワードハッシャーの初期化
pwd_hasher = PasswordHasher()


def hash_password(password: str) -> str:
    """Argon2id を使用してパスワードをハッシュ化する"""
    return pwd_hasher.hash(password)


def verify_password(password: str, hashed: str) -> bool:
    """Argon2id ハッシュとパスワードを検証する"""
    try:
        return pwd_hasher.verify(hashed, password)
    except (VerifyMismatchError, InvalidHash):
        return False


def generate_api_key() -> Tuple[str, str, str]:
    """
    OpenAI 互換の API キーを生成する。
    Returns:
        (raw_key, key_hash, prefix)
    """
    raw_key = f"sk-live-{secrets.token_hex(24)}"
    key_hash = hash_api_key(raw_key)
    prefix = f"{raw_key[:15]}..."
    return raw_key, key_hash, prefix


def hash_api_key(key: str) -> str:
    """API キーの SHA-256 ハッシュを計算する"""
    return hashlib.sha256(key.encode("utf-8")).hexdigest()


def generate_session_id() -> str:
    """暗号学的に安全なセッションIDを生成する"""
    return secrets.token_urlsafe(32)


def generate_pkce_pair() -> Tuple[str, str]:
    """
    PKCE (RFC 7636) 用の code_verifier と code_challenge (S256) を生成する。
    Returns:
        (code_verifier, code_challenge)
    """
    code_verifier = secrets.token_urlsafe(64)
    digest = hashlib.sha256(code_verifier.encode("ascii")).digest()
    code_challenge = base64.urlsafe_b64encode(digest).decode("ascii").rstrip("=")
    return code_verifier, code_challenge


def generate_random_token(length: int = 32) -> str:
    """state や nonce に用いる暗号学的乱数文字列を生成する"""
    return secrets.token_urlsafe(length)

