import hashlib
import base64
from fastapi import Response
from sqlmodel import Session, create_engine, SQLModel, select
from app.core.security import (
    generate_api_key,
    generate_pkce_pair,
    generate_random_token,
    hash_api_key,
    hash_password,
    verify_password,
)
from app.models.user import User
from app.models.session import SessionRecord
from app.api.web.auth import create_user_session


def test_argon2id_password_hashing():
    """Argon2id によるパスワードハッシュ生成と照合の検証"""
    password = "SuperSecretPassword123!"
    hashed = hash_password(password)

    assert hashed != password
    assert "$argon2id$" in hashed
    assert verify_password(password, hashed) is True
    assert verify_password("WrongPassword", hashed) is False


def test_api_key_generation_and_hashing():
    """API キー生成と SHA-256 ハッシュの整合性検証"""
    raw_key, key_hash, prefix = generate_api_key()

    assert raw_key.startswith("sk-live-")
    assert len(raw_key) > 30
    assert prefix.startswith("sk-live-")
    assert hash_api_key(raw_key) == key_hash


def test_pkce_generation():
    """PKCE (RFC 7636) code_verifier と code_challenge (S256) の検証"""
    code_verifier, code_challenge = generate_pkce_pair()

    assert len(code_verifier) >= 43
    # S256 の手動計算と一致するか確認
    expected_digest = hashlib.sha256(code_verifier.encode("ascii")).digest()
    expected_challenge = base64.urlsafe_b64encode(expected_digest).decode("ascii").rstrip("=")
    assert code_challenge == expected_challenge


def test_session_fixation_protection_and_cookie_attributes():
    """ログイン時のセッションID再生成（セッション固定攻撃対策）およびCookie属性の検証"""
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)

    with Session(engine) as db:
        user = User(
            id="user-uuid-1",
            auth_type="local",
            username="tester",
            password_hash=hash_password("testpass"),
        )
        db.add(user)
        db.commit()

        resp = Response()
        session_id_1 = create_user_session(db, user, resp)
        session_id_2 = create_user_session(db, user, resp)

        # ログイン呼び出し毎に必ず異なるセッションIDが生成されること
        assert session_id_1 != session_id_2

        # DB に登録されていることを確認
        assert db.get(SessionRecord, session_id_1) is not None
        assert db.get(SessionRecord, session_id_2) is not None

        # Cookie ヘッダーの属性検証 (HttpOnly, SameSite=lax)
        cookie_header = resp.headers.get("set-cookie", "")
        assert "HttpOnly" in cookie_header
        assert "SameSite=lax" in cookie_header or "samesite=lax" in cookie_header

