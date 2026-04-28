import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Copy, Check } from 'lucide-react'
import { useState } from 'react'
import type { Message } from '../api/types'

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  function copy() {
    navigator.clipboard.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }
  return (
    <button
      onClick={copy}
      title="Copy"
      className="rounded p-1 text-gray-600 hover:text-gray-300 transition-colors"
    >
      {copied ? <Check size={12} className="text-emerald-400" /> : <Copy size={12} />}
    </button>
  )
}

function CodeBlock({ children }: { children: string }) {
  return (
    <div className="group relative my-2 rounded-xl bg-surface border border-surface-border">
      <div className="absolute right-2 top-2 opacity-0 group-hover:opacity-100 transition-opacity">
        <CopyButton text={children} />
      </div>
      <pre className="overflow-x-auto p-4 font-mono text-[12px] text-gray-300 leading-relaxed">
        <code>{children}</code>
      </pre>
    </div>
  )
}

interface Props {
  message: Message
}

export default function ChatBubble({ message }: Props) {
  const isUser = message.role === 'user'
  const isTool = message.role === 'tool'

  // The API returns content as a plain string (already decoded by the history store).
  // Guard against any residual JSON-encoded strings just in case.
  let content = message.content ?? ''
  if (content.startsWith('"') && content.endsWith('"')) {
    try { content = JSON.parse(content) } catch { /* use raw */ }
  }

  if (isTool || message.role === 'raw_model_json') return null

  if (isUser) {
    return (
      <div className="flex justify-end mb-4 animate-slide-up">
        <div className="group relative max-w-[75%]">
          <div className="rounded-2xl rounded-tr-sm bg-accent px-4 py-3 text-sm text-white shadow-lg shadow-accent/20">
            {content}
          </div>
          <div className="absolute right-1 -bottom-5 opacity-0 group-hover:opacity-100 transition-opacity">
            <CopyButton text={content} />
          </div>
        </div>
      </div>
    )
  }

  // Assistant
  return (
    <div className="flex mb-4 animate-slide-up">
      <div className="group relative max-w-[85%]">
        <div className="rounded-2xl rounded-tl-sm bg-surface-overlay border border-surface-border px-4 py-3 text-sm text-gray-200 shadow-sm">
          <ReactMarkdown
            remarkPlugins={[remarkGfm]}
            components={{
              code({ className, children, ...rest }) {
                const isInline = !className
                const text = String(children).replace(/\n$/, '')
                if (isInline) {
                  return (
                    <code
                      className="rounded bg-surface px-1.5 py-0.5 font-mono text-[12px] text-indigo-300"
                      {...rest}
                    >
                      {text}
                    </code>
                  )
                }
                return <CodeBlock>{text}</CodeBlock>
              },
              p({ children }) { return <p className="mb-2 last:mb-0 leading-relaxed">{children}</p> },
              ul({ children }) { return <ul className="mb-2 list-disc pl-4 space-y-1">{children}</ul> },
              ol({ children }) { return <ol className="mb-2 list-decimal pl-4 space-y-1">{children}</ol> },
              li({ children }) { return <li className="leading-relaxed">{children}</li> },
              strong({ children }) { return <strong className="font-semibold text-gray-100">{children}</strong> },
              blockquote({ children }) {
                return (
                  <blockquote className="border-l-2 border-accent/50 pl-3 my-2 text-gray-400 italic">
                    {children}
                  </blockquote>
                )
              },
              a({ href, children }) {
                return <a href={href} target="_blank" rel="noreferrer" className="text-accent hover:underline">{children}</a>
              },
              table({ children }) {
                return (
                  <div className="my-2 overflow-x-auto rounded-lg border border-surface-border">
                    <table className="w-full text-xs">{children}</table>
                  </div>
                )
              },
              th({ children }) { return <th className="border-b border-surface-border bg-surface px-3 py-2 text-left font-semibold text-gray-300">{children}</th> },
              td({ children }) { return <td className="border-b border-surface-border px-3 py-2 text-gray-400">{children}</td> },
            }}
          >
            {content}
          </ReactMarkdown>
        </div>
        <div className="absolute right-1 -bottom-5 opacity-0 group-hover:opacity-100 transition-opacity">
          <CopyButton text={content} />
        </div>
      </div>
    </div>
  )
}
