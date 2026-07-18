import {
  useEffect,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { QrCode, RefreshCw, Repeat2, Send } from "lucide-react";
import {
  fetchAuthStatus,
  qrImageURL,
  startQRLogin,
  submitAuthPassword,
  switchAuthAccount,
  type AuthStatus,
} from "./api";
import "./AuthGate.css";

const AUTH_POLL_MS = 1000;

export function AuthGate({ children }: { children: ReactNode }) {
  const [auth, setAuth] = useState<AuthStatus | null>(null);
  const [pollVersion, setPollVersion] = useState(0);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [connectionError, setConnectionError] = useState("");
  const [showSwitcher, setShowSwitcher] = useState(false);

  useEffect(() => {
    let active = true;
    let timer = 0;

    async function poll() {
      try {
        let next = await fetchAuthStatus();
        if (next.status === "unauthorized") {
          next = await startQRLogin();
        }
        if (!active) return;
        setAuth(next);
        setConnectionError("");
        if (!next.authorized && next.status !== "error") {
          timer = window.setTimeout(poll, AUTH_POLL_MS);
        }
      } catch (err) {
        if (!active) return;
        setConnectionError(err instanceof Error ? err.message : String(err));
        timer = window.setTimeout(poll, AUTH_POLL_MS);
      }
    }

    void poll();
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, [pollVersion]);

  async function retryQR() {
    setBusy(true);
    setConnectionError("");
    try {
      setAuth(await startQRLogin());
      setPollVersion((value) => value + 1);
    } catch (err) {
      setConnectionError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  async function submitPassword(event: FormEvent) {
    event.preventDefault();
    if (!password) return;
    setBusy(true);
    setConnectionError("");
    try {
      setAuth(await submitAuthPassword(password));
      setPassword("");
    } catch (err) {
      setConnectionError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  async function switchAccount(previous = false) {
    setBusy(true);
    setConnectionError("");
    try {
      await switchAuthAccount(previous);
      setAuth(null);
      setShowSwitcher(false);
      setPollVersion((value) => value + 1);
    } catch (err) {
      setConnectionError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  if (auth?.authorized) {
    return (
      <>
        {children}
        <button
          type="button"
          className="auth-account-switch"
          onClick={() => setShowSwitcher((open) => !open)}
          disabled={busy}
          title="切换 Telegram 账号"
          aria-label="切换 Telegram 账号"
          aria-expanded={showSwitcher}
        >
          <Repeat2 size={18} aria-hidden="true" />
        </button>
        {showSwitcher && (
          <div className="auth-switch-menu" role="menu">
            {auth.can_switch_back && (
              <button
                type="button"
                onClick={() => void switchAccount(true)}
                disabled={busy}
                role="menuitem"
              >
                <Repeat2 size={16} aria-hidden="true" />
                切回上一个账号
              </button>
            )}
            <button
              type="button"
              onClick={() => void switchAccount()}
              disabled={busy}
              role="menuitem"
            >
              <QrCode size={16} aria-hidden="true" />
              扫码切换新账号
            </button>
          </div>
        )}
      </>
    );
  }

  const waiting =
    !auth ||
    auth.status === "checking" ||
    auth.status === "authorizing" ||
    (auth.status === "waiting_scan" && !auth.qr_revision);

  return (
    <div className="auth-screen">
      <header className="auth-topbar">
        <div className="auth-wordmark">
          BOC <span>PREVIEW</span>
        </div>
        <div className="auth-section-label">TELEGRAM ACCESS</div>
      </header>
      <main className="auth-card">
        {auth?.status === "password_required" ? (
          <div className="auth-password-panel">
            <div className="auth-brand">
              <Send size={34} aria-hidden="true" />
            </div>
            <h1>两步验证</h1>
            <p className="auth-lead">请输入此 Telegram 账号的两步验证密码。</p>
            <form className="auth-password-form" onSubmit={submitPassword}>
              <label htmlFor="telegram-password">密码</label>
              <input
                id="telegram-password"
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                autoComplete="current-password"
                autoFocus
                disabled={busy}
              />
              <button type="submit" disabled={!password || busy}>
                {busy ? "验证中…" : "继续"}
              </button>
            </form>
          </div>
        ) : (
          <div className="auth-login-grid">
            <div className="auth-qr">
              {waiting ? (
                <RefreshCw
                  className="auth-spinner"
                  size={42}
                  aria-label="正在生成二维码"
                />
              ) : auth?.qr_revision ? (
                <>
                  <img
                    key={auth.qr_revision}
                    src={qrImageURL(auth.qr_revision)}
                    alt="Telegram 登录二维码"
                  />
                </>
              ) : (
                <RefreshCw size={42} aria-hidden="true" />
              )}
            </div>
            <div className="auth-copy">
              <div className="auth-kicker">LOGIN</div>
              <h1>使用 Telegram 登录</h1>
              <ol className="auth-steps">
                <li>在手机上打开 Telegram</li>
                <li>进入“设置” → “设备”</li>
                <li>点击“连接桌面设备”并扫描二维码</li>
              </ol>
              <p className="auth-note">二维码过期后会自动刷新。</p>
              {auth?.can_switch_back && (
                <button
                  type="button"
                  className="auth-switch-back"
                  onClick={() => void switchAccount(true)}
                  disabled={busy}
                >
                  <Repeat2 size={16} aria-hidden="true" />
                  切回之前的账号
                </button>
              )}
            </div>
          </div>
        )}

        {(connectionError || auth?.error) && (
          <div className="auth-error" role="alert">
            {connectionError || auth?.error}
          </div>
        )}
        {auth?.status === "error" && (
          <button
            type="button"
            className="auth-retry"
            onClick={retryQR}
            disabled={busy}
          >
            <RefreshCw size={16} aria-hidden="true" />
            重新生成二维码
          </button>
        )}
      </main>
    </div>
  );
}
