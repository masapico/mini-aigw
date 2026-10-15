import multiprocessing
import os
import time
import pytest
import uvicorn
from playwright.sync_api import sync_playwright
from sqlmodel import Session, select
from app.core.database import engine, init_db
from app.core.security import hash_password
from app.models.group import Group
from app.models.user import User

SERVER_PORT = 8888
BASE_URL = f"http://127.0.0.1:{SERVER_PORT}"


def run_test_server():
    from app.main import app
    uvicorn.run(app, host="127.0.0.1", port=SERVER_PORT, log_level="warning")


@pytest.fixture(scope="module")
def live_server():
    init_db()
    with Session(engine) as session:
        # 管理者初期化
        admin = session.exec(select(User).where(User.username == "admin")).first()
        if not admin:
            admin = User(
                auth_type="local",
                username="admin",
                password_hash=hash_password("admin123"),
                roles='["admin"]',
                groups='["sales", "dev"]',
            )
            session.add(admin)
            session.commit()

        # グループ初期化
        if not session.exec(select(Group)).all():
            session.add(Group(name="sales", display_name="営業部 (sales)"))
            session.add(Group(name="dev", display_name="開発部 (dev)"))
            session.commit()

    proc = multiprocessing.Process(target=run_test_server, daemon=True)
    proc.start()

    # サーバー立ち上がり待機
    import urllib.request
    started = False
    for _ in range(30):
        try:
            with urllib.request.urlopen(f"{BASE_URL}/login") as resp:
                if resp.status == 200:
                    started = True
                    break
        except Exception:
            time.sleep(0.2)

    assert started, "Test server failed to start"
    yield BASE_URL
    proc.terminate()


def test_playwright_knowledge_tag_ux_and_alignment(live_server):
    """
    Playwright による UI 検証:
    1. ログインからナレッジ管理画面への遷移
    2. タグ入力 + Enter でバッジ追加、バッジクリックで削除できることの検証
    3. テキストボックス・セレクトボックス・ボタンの水平アラインメント（高さ・Y座標のズレ）検証
    4. スクリーンショット保存
    """
    os.makedirs("tests/screenshots", exist_ok=True)

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        context = browser.new_context(viewport={"width": 1280, "height": 900})
        page = context.new_page()

        # 1. ログイン
        page.goto(f"{live_server}/login")
        page.fill('input[name="username"]', "admin")
        page.fill('input[name="password"]', "admin123")
        page.click('button[type="submit"]')
        page.wait_for_url(f"{live_server}/")
        assert "システム概要" in page.content()

        # 2. ナレッジ管理画面へ遷移し、折りたたまれたアップロードカードを開く
        page.goto(f"{live_server}/knowledge")
        page.wait_for_selector("#btn-toggle-upload")
        page.click("#btn-toggle-upload")
        page.wait_for_selector("#upload-form", state="visible")

        # 水平アラインメント検証: 1行目のセレクトボックスとタグ入力ボックスの上端・高さ
        group_select = page.locator('select[name="group_name"]')
        tag_input = page.locator('#tag-input-box')
        tag_add_btn = page.locator('#tag-add-btn')

        box_select = group_select.bounding_box()
        box_tag_input = tag_input.bounding_box()
        box_tag_btn = tag_add_btn.bounding_box()

        assert box_select is not None and box_tag_input is not None and box_tag_btn is not None

        # 高さの一致検証 (42px 前後で統一)
        assert abs(box_select["height"] - box_tag_input["height"]) < 2, f"Height mismatch: select={box_select['height']}, tag_input={box_tag_input['height']}"
        assert abs(box_tag_input["height"] - box_tag_btn["height"]) < 2, f"Height mismatch: tag_input={box_tag_input['height']}, tag_btn={box_tag_btn['height']}"

        # 2行目のファイル入力とアップロードボタンの高さ・下端整列検証
        file_input = page.locator('#file-input')
        upload_btn = page.locator('#upload-form button[type="submit"]')
        box_file = file_input.bounding_box()
        box_upload = upload_btn.bounding_box()

        assert box_file is not None and box_upload is not None
        assert abs(box_file["height"] - box_upload["height"]) < 2, f"Height mismatch: file={box_file['height']}, upload_btn={box_upload['height']}"
        # 下端Y座標が水平に揃っていること (align-items-end)
        bottom_file = box_file["y"] + box_file["height"]
        bottom_upload = box_upload["y"] + box_upload["height"]
        assert abs(bottom_file - bottom_upload) < 2, f"Bottom misalignment: file={bottom_file}, upload={bottom_upload}"

        # 3. タグ UX 検証: 入力 + Enter でバッジ追加
        tag_input.fill("2024年度")
        tag_input.press("Enter")
        page.wait_for_timeout(100)

        tag_input.fill("業務規程")
        tag_input.press("Enter")
        page.wait_for_timeout(100)

        # 追加ボタンによる追加も確認
        tag_input.fill("重要")
        tag_add_btn.click()
        page.wait_for_timeout(100)

        # バッジが3つ表示されていること
        badges = page.locator("#tag-badges-container .tag-pill")
        assert badges.count() == 3
        badge_texts = [badges.nth(i).text_content() for i in range(badges.count())]
        assert any("2024年度" in t for t in badge_texts)
        assert any("業務規程" in t for t in badge_texts)
        assert any("重要" in t for t in badge_texts)

        # hidden input に JSON 文字列が格納されていること
        hidden_val = page.locator('#tags-hidden-input').input_value()
        assert "2024年度" in hidden_val
        assert "業務規程" in hidden_val
        assert "重要" in hidden_val

        # バッジクリックで削除できることの検証 (「業務規程」を削除)
        rule_badge = badges.filter(has_text="業務規程")
        rule_badge.click()
        page.wait_for_timeout(100)

        assert badges.count() == 2
        new_hidden_val = page.locator('#tags-hidden-input').input_value()
        assert "業務規程" not in new_hidden_val
        assert "2024年度" in new_hidden_val

        # スクリーンショット保存
        page.screenshot(path="tests/screenshots/knowledge_page.png")

        # 4. ユーザー管理画面のレイアウト・水平アラインメント検証
        page.goto(f"{live_server}/users")
        page.wait_for_selector("#create-user-form")

        u_input = page.locator('#new-username-input').bounding_box()
        p_input = page.locator('#new-password-input').bounding_box()
        assert u_input is not None and p_input is not None
        assert abs(u_input["height"] - p_input["height"]) < 2
        assert abs(u_input["y"] - p_input["y"]) < 2, f"User row 1 vertical mismatch: {u_input['y']} vs {p_input['y']}"

        page.screenshot(path="tests/screenshots/users_page.png")

        # 所属グループタブの確認
        page.click("#groups-tab")
        page.wait_for_selector("#create-group-form", state="visible")
        page.wait_for_timeout(300)

        grp_name = page.locator('#grp-name-input').bounding_box()
        grp_disp = page.locator('#grp-disp-input').bounding_box()
        assert grp_name is not None and grp_disp is not None
        assert abs(grp_name["height"] - grp_disp["height"]) < 2

        page.screenshot(path="tests/screenshots/groups_tab.png")

        # 5. API キー発行画面の水平アラインメント検証
        page.goto(f"{live_server}/api-keys")
        page.wait_for_selector("#keyNameInput")

        key_input = page.locator('#keyNameInput').bounding_box()
        model_select = page.locator('#keyModelSelect').bounding_box()
        grp_box = page.locator('#key-groups-container').bounding_box()
        key_btn = page.locator('form[hx-post="/api-keys/create"] button[type="submit"]').bounding_box()
        prompt_input = page.locator('#keySystemPromptInput').bounding_box()

        assert key_input is not None and model_select is not None and grp_box is not None and key_btn is not None and prompt_input is not None

        # 1行目: key_input と model_select のY座標と高さが揃っていること
        assert abs(key_input["y"] - model_select["y"]) < 2
        assert abs(key_input["height"] - model_select["height"]) < 2

        # 2行目: prompt_input と key_btn の下端が揃っていること (align-items-end)
        bottom_prompt = prompt_input["y"] + prompt_input["height"]
        bottom_btn = key_btn["y"] + key_btn["height"]
        assert abs(bottom_prompt - bottom_btn) < 2, f"API Key prompt and button bottom mismatch: {bottom_prompt} vs {bottom_btn}"

        page.screenshot(path="tests/screenshots/api_keys_page.png")

        # 5-1. 新規フォームで「この指示例を挿入」ボタンをクリック
        page.click("button:has-text('この指示例を挿入')")
        inserted_prompt = page.locator('#keySystemPromptInput').input_value()
        assert "参照可能なナレッジ" in inserted_prompt
        assert "参照可能なナレッジに記載がありません" in inserted_prompt

        # 5-2. 一覧の編集ボタンをクリックしてモーダルを開く
        edit_buttons = page.locator('button:has-text("編集")')
        if edit_buttons.count() > 0:
            edit_buttons.first.click()
            page.wait_for_selector(".modal.show", state="visible")
            page.wait_for_timeout(300)
            page.screenshot(path="tests/screenshots/api_keys_edit_modal.png")

            # モーダル内の「💡 ナレッジ優先回答の指示例を挿入」ボタンをクリック
            modal_prompt_btn = page.locator('.modal.show button:has-text("💡 ナレッジ優先回答の指示例を挿入")')
            modal_prompt_btn.click()
            modal_textarea = page.locator('.modal.show textarea[name="system_prompt"]')
            assert "参照可能なナレッジ" in modal_textarea.input_value()

            # モーダルの「保存」ボタンをクリック
            page.locator('.modal.show button[type="submit"]:has-text("保存")').click()
            page.wait_for_selector(".modal.show", state="hidden")
            page.wait_for_timeout(300)

        # 6. モデル管理画面の検証
        page.goto(f"{live_server}/models")
        page.wait_for_selector("#models-container")
        page.screenshot(path="tests/screenshots/models_page.png")

        browser.close()
