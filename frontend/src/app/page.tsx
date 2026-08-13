"use client";

import React, { useState, useRef, useEffect } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { errMessage } from '@/lib/errMessage';
import { useTheme } from '@/context/ThemeContext';
import { ConfirmDialog, useToast } from '@/components/ui';

interface ChatMessage {
  id: string;
  sender: 'user' | 'ai';
  text: string;
  actionCard?: {
    title: string;
    description: string;
    actionLabel: string;
    onExecute: () => void;
  };
}

// A remediation action awaiting the operator's explicit confirmation before it
// executes — the confirm→run→result flow that replaces the old blocking alert()s.
interface PendingAction {
  title: string;
  type: string;
  target: string;
  parameters: Record<string, unknown>;
}

export default function ConversationalSRE() {
  const { tokens: t } = useTheme();
  // Previously opened with a hardcoded fake finding ("I noticed a 15% increase
  // in error rates on cart-service") regardless of what was actually happening
  // in the cluster — anyone testing this would eventually notice the number
  // never changes. The opener now makes no unverified claims; real findings
  // only appear once the backend actually returns them via handleSend.
  const [messages, setMessages] = useState<ChatMessage[]>([
    {
      id: '1',
      sender: 'ai',
      text: 'Hello. I am PulseTrace, your Autonomous SRE. Ask me about a service, an incident, or tell me what to fix.'
    }
  ]);
  const [inputValue, setInputValue] = useState('');
  const [isTyping, setIsTyping] = useState(false);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const toast = useToast();

  // Confirm→run→result state for executing a remediation action (F1: replaces
  // the blocking alert() calls with an accessible confirm dialog + toasts).
  const [pendingAction, setPendingAction] = useState<PendingAction | null>(null);
  const [executing, setExecuting] = useState(false);

  const executeAction = async () => {
    if (!pendingAction) return;
    setExecuting(true);
    try {
      // action-service's ExecuteRequest expects {action_type, target, parameters}.
      const res = await fetchWithAuth('/api/v1/actions/execute', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          action_type: pendingAction.type,
          target: pendingAction.target,
          parameters: pendingAction.parameters,
        }),
      });
      if (res.ok) {
        toast.success(`Executed ${pendingAction.type} on ${pendingAction.target}`);
        setMessages((prev) => [...prev, {
          id: Date.now().toString(),
          sender: 'ai',
          text: `✅ Executed ${pendingAction.type} on ${pendingAction.target}.`,
        }]);
      } else {
        const detail = await res.text().catch(() => '');
        const msg = `Action failed (${res.status}).${detail ? ' ' + detail : ''}`;
        toast.error(msg);
        setMessages((prev) => [...prev, { id: Date.now().toString(), sender: 'ai', text: `⚠️ ${msg}` }]);
      }
    } catch (err) {
      // Never report fake success on a transport failure — the operator must know
      // the action never reached the backend.
      const msg = `Could not reach PulseTrace Operator: ${errMessage(err, 'unknown error')}`;
      toast.error(msg);
      setMessages((prev) => [...prev, { id: Date.now().toString(), sender: 'ai', text: `⚠️ ${msg}` }]);
    } finally {
      setExecuting(false);
      setPendingAction(null);
    }
  };

  const scrollToBottom = () => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  };

  useEffect(() => {
    scrollToBottom();
  }, [messages]);

  const handleSend = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!inputValue.trim()) return;

    const userMsg: ChatMessage = { id: Date.now().toString(), sender: 'user', text: inputValue };
    setMessages(prev => [...prev, userMsg]);
    setInputValue('');
    setIsTyping(true);

    try {
      const res = await fetchWithAuth('/api/v1/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message: userMsg.text })
      });

      if (!res.ok) {
        throw new Error(`API error: ${res.status}`);
      }

      const data = await res.json();
      
      const aiResponse: ChatMessage = {
        id: (Date.now() + 1).toString(),
        sender: 'ai',
        text: data.text || 'I encountered an issue generating a response.',
      };

      if (data.actionCard) {
        aiResponse.actionCard = {
          title: data.actionCard.title,
          description: data.actionCard.description,
          actionLabel: data.actionCard.actionLabel,
          // Open an accessible confirm dialog; execution happens in executeAction
          // after the operator confirms (confirm→run→result, no blocking alert()).
          onExecute: () => setPendingAction({
            title: data.actionCard.title,
            type: data.actionCard.type,
            target: data.actionCard.target,
            parameters: data.actionCard.parameters || {},
          }),
        };
      }

      setMessages(prev => [...prev, aiResponse]);
    } catch (err) {
      console.error(err);
      setMessages(prev => [...prev, {
        id: Date.now().toString(),
        sender: 'ai',
        text: 'Sorry, I am currently unable to reach the PulseTrace AI Engine. Ensure the backend services are running.'
      }]);
    } finally {
      setIsTyping(false);
    }
  };

  // Grounded suggested prompts (AI-SRE · E4): seeded from live tenant state
  // (open incidents first) via the backend, with a static fallback so the empty
  // state always has chips even if the suggestions call fails.
  const [suggestionChips, setSuggestionChips] = useState<string[]>([
    'Which services have the highest error rate in the last hour?',
    'Were there any deploys in the last 24 hours?',
    'Give me a health summary of all services',
  ]);

  useEffect(() => {
    fetchWithAuth('/api/v1/chat/suggestions')
      .then(res => (res.ok ? res.json() : null))
      .then(data => {
        const s: string[] = data?.suggestions || [];
        if (s.length > 0) setSuggestionChips(s);
      })
      .catch(() => { /* keep the static fallback chips */ });
  }, []);

  const sendSuggestion = (text: string) => {
    setInputValue(text);
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: 'calc(100vh - 120px)', maxWidth: '880px', margin: '0 auto', width: '100%' }}>

      {/* Header */}
      <div style={{ marginBottom: '24px', textAlign: 'center' }}>
        <h1 style={{
          fontSize: '34px',
          fontWeight: 800,
          margin: '4px 0 8px',
          letterSpacing: '-0.01em',
          background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
          WebkitBackgroundClip: 'text',
          WebkitTextFillColor: 'transparent',
        }}>PulseTrace Autonomous SRE</h1>
        <p style={{ color: t.text2, fontSize: '15px' }}>Don&apos;t look at charts. Ask questions and execute fixes.</p>
      </div>

      {/* Chat Area */}
      <div style={{
        flex: 1,
        display: 'flex',
        flexDirection: 'column',
        overflow: 'hidden',
        borderRadius: '24px',
        background: t.panelBg,
        border: `1px solid ${t.panelBorder}`,
        borderTop: `1px solid ${t.panelTop}`,
        backdropFilter: 'blur(28px) saturate(180%)',
        WebkitBackdropFilter: 'blur(28px) saturate(180%)',
        boxShadow: t.shadow,
      }}>

        {/* Message Feed */}
        <div style={{ flex: 1, overflowY: 'auto', padding: '32px', display: 'flex', flexDirection: 'column', gap: '24px' }}>
          {messages.map(msg => {
            const isUser = msg.sender === 'user';
            return (
              <div key={msg.id} style={{ display: 'flex', justifyContent: isUser ? 'flex-end' : 'flex-start' }}>

                <div style={{
                  maxWidth: '75%',
                  display: 'flex',
                  gap: '14px',
                  alignItems: 'flex-start',
                  flexDirection: isUser ? 'row-reverse' : 'row'
                }}>
                  {/* Avatar */}
                  <div style={{
                    width: '34px',
                    height: '34px',
                    borderRadius: '50%',
                    background: isUser ? t.accent : `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    fontSize: '15px',
                    fontWeight: 700,
                    color: '#fff',
                    flexShrink: 0
                  }}>
                    {isUser ? 'U' : '✦'}
                  </div>

                  {/* Message Bubble */}
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '12px', alignItems: isUser ? 'flex-end' : 'flex-start' }}>
                    <div style={{
                      background: isUser ? t.accent : t.panelBg,
                      color: isUser ? '#fff' : t.text1,
                      padding: '15px 19px',
                      borderRadius: '17px',
                      borderTopLeftRadius: isUser ? '17px' : '5px',
                      borderTopRightRadius: isUser ? '5px' : '17px',
                      lineHeight: 1.6,
                      fontSize: '14.5px',
                      border: isUser ? 'none' : `1px solid ${t.panelBorder}`
                    }}>
                      {msg.text}
                    </div>

                    {/* Action Card */}
                    {msg.actionCard && (
                      <div style={{
                        background: t.redSoft,
                        border: `1px solid ${t.red}33`,
                        padding: '20px',
                        borderRadius: '16px',
                        width: '100%',
                      }}>
                        <h4 style={{ fontSize: '15px', fontWeight: 700, color: t.red, marginBottom: '8px' }}>{msg.actionCard.title}</h4>
                        <p style={{ color: t.text2, fontSize: '13.5px', lineHeight: 1.5, marginBottom: '16px' }}>{msg.actionCard.description}</p>
                        <button
                          onClick={msg.actionCard.onExecute}
                          style={{
                            background: t.red,
                            color: '#fff',
                            border: 'none',
                            padding: '11px 20px',
                            borderRadius: '10px',
                            fontWeight: 600,
                            fontSize: '13.5px',
                            cursor: 'pointer',
                            width: '100%'
                          }}
                        >
                          {msg.actionCard.actionLabel}
                        </button>
                      </div>
                    )}
                  </div>

                </div>
              </div>
            );
          })}
          {isTyping && (
             <div style={{ display: 'flex', justifyContent: 'flex-start' }}>
                <div style={{ display: 'flex', gap: '14px', alignItems: 'center' }}>
                  <div style={{
                    width: '34px',
                    height: '34px',
                    borderRadius: '50%',
                    background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    fontSize: '15px',
                    fontWeight: 700,
                    color: '#fff'
                  }}>✦</div>
                  <div style={{ color: t.text2, fontStyle: 'italic', fontSize: '14px' }}>PulseTrace is thinking…</div>
                </div>
             </div>
          )}
          <div ref={messagesEndRef} />
        </div>

        {/* Input Area */}
        <div style={{ padding: '20px 24px', borderTop: `1px solid ${t.panelBorder}` }}>
          <form onSubmit={handleSend} style={{ display: 'flex', gap: '12px' }}>
            <input
              type="text"
              value={inputValue}
              onChange={e => setInputValue(e.target.value)}
              placeholder="Ask a question or execute a runbook (e.g. 'Why is cart-service failing?')"
              style={{
                flex: 1,
                background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.6)',
                border: `1px solid ${t.panelBorder}`,
                padding: '15px 22px',
                borderRadius: '100px',
                color: t.text1,
                fontSize: '14.5px',
                outline: 'none'
              }}
            />
            <button
              type="submit"
              disabled={!inputValue.trim() || isTyping}
              style={{
                background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
                color: '#fff',
                border: 'none',
                borderRadius: '100px',
                padding: '0 32px',
                fontWeight: 600,
                fontSize: '14.5px',
                cursor: (!inputValue.trim() || isTyping) ? 'not-allowed' : 'pointer',
                opacity: (!inputValue.trim() || isTyping) ? 0.6 : 1,
              }}
            >
              Send
            </button>
          </form>
          <div data-testid="suggestion-chips" style={{ display: 'flex', gap: '12px', marginTop: '16px', justifyContent: 'center', flexWrap: 'wrap' }}>
            {suggestionChips.map(chip => (
              <button
                key={chip}
                type="button"
                onClick={() => sendSuggestion(chip)}
                style={{
                  fontSize: '12.5px',
                  color: t.text2,
                  background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.6)',
                  border: `1px solid ${t.panelBorder}`,
                  padding: '6px 14px',
                  borderRadius: '100px',
                  cursor: 'pointer'
                }}
              >
                &quot;{chip}&quot;
              </button>
            ))}
          </div>
        </div>

      </div>

      <ConfirmDialog
        open={pendingAction !== null}
        danger
        busy={executing}
        title="Execute this remediation action?"
        body={
          pendingAction ? (
            <span>
              Run <strong style={{ color: t.text1 }}>{pendingAction.type}</strong> on{' '}
              <strong style={{ color: t.text1 }}>{pendingAction.target}</strong>. This makes a real
              change via the PulseTrace Operator and cannot be undone automatically.
            </span>
          ) : null
        }
        confirmLabel="Execute"
        onConfirm={executeAction}
        onCancel={() => setPendingAction(null)}
      />
    </div>
  );
}
