import logging
import urllib.parse
from typing import Any, Dict, Optional, Tuple
import httpx
from app.core.config import settings
from app.core.security import generate_pkce_pair, generate_random_token

logger = logging.getLogger(__name__)


class OidcService:
    def __init__(self):
        self.issuer = settings.OIDC_ISSUER
        self.client_id = settings.OIDC_CLIENT_ID
        self.client_secret = settings.OIDC_CLIENT_SECRET
        self.redirect_uri = settings.OIDC_REDIRECT_URI
        self.scopes = settings.OIDC_SCOPES
        self.end_session_endpoint = settings.OIDC_END_SESSION_ENDPOINT

    def is_configured(self) -> bool:
        """mini-id OIDC 設定が有効か判定する"""
        return bool(self.issuer and self.client_id)

    def create_authorization_request(self) -> Tuple[str, str, str, str]:
        """
        認可リクエスト用の URL とパラメータ (state, nonce, code_verifier) を生成する。
        Returns:
            (auth_url, state, nonce, code_verifier)
        """
        state = generate_random_token(32)
        nonce = generate_random_token(32)
        code_verifier, code_challenge = generate_pkce_pair()

        params = {
            "response_type": "code",
            "client_id": self.client_id,
            "redirect_uri": self.redirect_uri,
            "scope": self.scopes,
            "state": state,
            "nonce": nonce,
            "code_challenge": code_challenge,
            "code_challenge_method": "S256",
        }

        auth_endpoint = f"{self.issuer.rstrip('/')}/protocol/openid-connect/auth" if self.issuer else "/oauth/authorize"
        auth_url = f"{auth_endpoint}?{urllib.parse.urlencode(params)}"
        return auth_url, state, nonce, code_verifier

    async def exchange_code(
        self,
        code: str,
        code_verifier: str,
    ) -> Dict[str, Any]:
        """
        認可コードと code_verifier を用いてトークンエンドポイントからトークンを取得する。
        """
        token_endpoint = f"{self.issuer.rstrip('/')}/protocol/openid-connect/token" if self.issuer else "/oauth/token"
        data = {
            "grant_type": "authorization_code",
            "code": code,
            "redirect_uri": self.redirect_uri,
            "client_id": self.client_id,
            "code_verifier": code_verifier,
        }
        if self.client_secret:
            data["client_secret"] = self.client_secret

        async with httpx.AsyncClient(timeout=10.0) as client:
            resp = await client.post(token_endpoint, data=data)
            resp.raise_for_status()
            return resp.json()

    def build_end_session_url(
        self,
        id_token_hint: Optional[str] = None,
        post_logout_redirect_uri: Optional[str] = None,
    ) -> Optional[str]:
        """
        RP-Initiated Logout 用の URL を生成する。
        """
        endpoint = self.end_session_endpoint
        if not endpoint and self.issuer:
            endpoint = f"{self.issuer.rstrip('/')}/protocol/openid-connect/logout"
        if not endpoint:
            return None

        params = {}
        if id_token_hint:
            params["id_token_hint"] = id_token_hint
        if post_logout_redirect_uri:
            params["post_logout_redirect_uri"] = post_logout_redirect_uri

        if params:
            return f"{endpoint}?{urllib.parse.urlencode(params)}"
        return endpoint


oidc_service = OidcService()

