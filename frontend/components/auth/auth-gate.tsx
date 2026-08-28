"use client"

import { useAuth } from "@/lib/auth-context"
import { LoginPage } from "@/components/auth/login-page"
import { Button } from "@/components/ui/button"
import type { ReactNode } from "react"

/**
 * AuthGate 把根渲染分成三态：
 *   loading       本地有 token 但还没验完 — 显示占位
 *   anonymous     未登录 — 显示登录页
 *   authenticated 已登录 — 显示业务内容
 */
export function AuthGate({ children }: { children: ReactNode }) {
  const { status } = useAuth()

  if (status === "loading") {
    return (
      <div className="flex min-h-screen items-center justify-center text-sm text-muted-foreground">
        加载中…
      </div>
    )
  }
  if (status === "anonymous") {
    return <LoginPage />
  }
  if (status === "unavailable") {
    return (
      <div className="flex min-h-screen flex-col items-center justify-center gap-4 px-4 text-center">
        <div className="space-y-1">
          <h1 className="text-xl font-semibold">后端未连接</h1>
          <p className="text-sm text-muted-foreground">请确认 UpstreamHub 后端服务已启动。</p>
        </div>
        <Button type="button" onClick={() => window.location.reload()}>重新连接</Button>
      </div>
    )
  }
  return <>{children}</>
}
