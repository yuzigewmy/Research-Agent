import { useEffect, useId, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage, ProjectApiError } from './projects'
import {
  evaluateQuestionnairePath, getPublicQuestionnaire, isQuestionnaireReceipt, questionnaireAnswerLimit,
  questionnaireTextFits, sendQuestionnaireResponse,
} from './questionnaires'
import type { QuestionnaireDefinition, QuestionnaireReceipt } from './questionnaires'
import './questionnaire.css'

function readReceipt(key: string) {
  try {
    const receipt: unknown = JSON.parse(sessionStorage.getItem(key) ?? 'null')
    return isQuestionnaireReceipt(receipt) ? receipt : undefined
  } catch {
    return undefined
  }
}

function isClosed(error: unknown) {
  return error instanceof ProjectApiError && error.status === 410 && error.code === 'QUESTIONNAIRE_CLOSED'
}

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
}

export default function QuestionnaireRespond({ publicId }: { publicId: string }) {
  const id = useId()
  const key = `research.questionnaire.receipt.${publicId}`
  const [receipt, setReceipt] = useState<QuestionnaireReceipt | undefined>(() => readReceipt(key))
  const receiptRef = useRef(receipt)
  const [definition, setDefinition] = useState<QuestionnaireDefinition>()
  const [closed, setClosed] = useState(false)
  const [readError, setReadError] = useState('')
  const [reload, setReload] = useState(0)
  const [answers, setAnswers] = useState(() => new Map<string, string>())
  const [answerError, setAnswerError] = useState<{ id: string; message: string }>()
  const [sendError, setSendError] = useState('')
  const [sending, setSending] = useState(false)
  const attempt = useRef<{ requestId: string; payload: string } | undefined>(undefined)
  const submission = useRef<AbortController | null>(null)
  const complete = receipt !== undefined
  const path = definition ? evaluateQuestionnairePath(definition.questions, answers) : undefined
  const hasBranches = definition?.questions.some((question) => (question.branches?.length ?? 0) > 0) ?? false

  useEffect(() => {
    if (complete) return
    const controller = new AbortController()
    void getPublicQuestionnaire(publicId, requestSignal(controller)).then((next) => {
      if (!controller.signal.aborted) setDefinition(next)
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return
      if (isClosed(error)) setClosed(true)
      else setReadError(errorMessage(error))
    })
    return () => controller.abort()
  }, [complete, publicId, reload])

  useEffect(() => () => submission.current?.abort(), [])

  function updateAnswer(question: string, answer: string) {
    const nextAnswers = new Map(answers).set(question, answer)
    setAnswers(nextAnswers)
    const nextVisible = definition ? evaluateQuestionnairePath(definition.questions, nextAnswers).visibleIds : undefined
    setAnswerError((current) => current && (current.id === question || (nextVisible && !nextVisible.has(current.id))) ? undefined : current)
    setSendError('')
  }

  async function send(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!definition || !path || receiptRef.current || closed || submission.current) return
    const responses: [string, string][] = []
    for (const { question } of path.visibleQuestions) {
      const answer = (answers.get(question.id) ?? '').trim()
      const message = question.required && !answer ? '请回答此题。'
        : !questionnaireTextFits(answer, questionnaireAnswerLimit) ? '回答最多 320 个 UTF-8 字节，且不能含空字符。'
          : answer && question.type === 'single' && !question.options?.includes(answer) ? '请选择一个有效选项。' : ''
      if (message) {
        setAnswerError({ id: question.id, message })
        document.getElementById(`${id}-${question.id}`)?.focus()
        return
      }
      if (answer) responses.push([question.id, answer])
    }
    const normalized = Object.fromEntries(responses)
    const payload = JSON.stringify(normalized)
    if (attempt.current?.payload !== payload) {
      const bytes = crypto.getRandomValues(new Uint8Array(16))
      const requestId = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
      attempt.current = { requestId, payload }
    }
    const controller = new AbortController()
    submission.current = controller
    setAnswerError(undefined)
    setSendError('')
    setSending(true)
    try {
      const next = await sendQuestionnaireResponse(publicId, attempt.current.requestId, normalized, requestSignal(controller))
      if (controller.signal.aborted) return
      receiptRef.current = next
      setReceipt(next)
      try {
        sessionStorage.setItem(key, JSON.stringify(next))
      } catch {
        // The receipt remains visible if session storage is unavailable.
      }
    } catch (error) {
      if (controller.signal.aborted) return
      if (isClosed(error)) setClosed(true)
      else setSendError(errorMessage(error))
    } finally {
      submission.current = null
      if (!controller.signal.aborted) setSending(false)
    }
  }

  return <main className="workbench questionnaire-respond">
    <header className="masthead"><h1 dir="auto">{definition?.title ?? '互动问卷'}</h1></header>
    <div className="project-panel">
      {receipt ? <div role="status">
        <h2>答卷已提交</h2>
        <p className="questionnaire-note">回执编号 <bdi>{receipt.responseId}</bdi></p>
      </div> : closed ? <p role="status">本问卷已停止回收。</p>
        : readError ? <div className="request-error">
          <p role="alert">{readError}</p>
          <button className="button outline" type="button" onClick={() => {
            setReadError('')
            setReload((value) => value + 1)
          }}>重试读取问卷</button>
        </div> : definition && path ? <>
          {definition.intro && <p className="questionnaire-intro" dir="auto">{definition.intro}</p>}
          {hasBranches && <p className="questionnaire-note" role="status">
            当前显示 {path.visibleQuestions.length} / {definition.questions.length} 题
            {path.skippedQuestions.length > 0 && `（已按选项跳过第 ${path.skippedQuestions.map(({ index }) => index + 1).join('、')} 题）`}
          </p>}
          <form className="create-form" noValidate aria-busy={sending} onSubmit={(event) => void send(event)}>
            {path.visibleQuestions.map(({ question, index }) => {
              const field = `${id}-${question.id}`
              const problem = answerError?.id === question.id ? answerError.message : undefined
              const value = answers.get(question.id) ?? ''
              const label = <>{index + 1}. <bdi>{question.label}</bdi>{question.required && '（必答）'}</>
              return question.type === 'single' ? (
                <fieldset key={question.id} className="questionnaire-question field" disabled={sending}
                  aria-describedby={problem ? `${field}-error` : undefined} aria-invalid={!!problem || undefined}>
                  <legend>{label}</legend>
                  {question.options?.map((option, optionIndex) => <label className="questionnaire-choice" key={option}>
                    <input id={optionIndex === 0 ? field : undefined} type="radio" name={field} value={option} checked={value === option}
                      required={question.required} onChange={() => updateAnswer(question.id, option)} />
                    <bdi>{option}</bdi>
                  </label>)}
                  {!question.required && <label className="questionnaire-choice">
                    <input type="radio" name={field} value="" checked={!value} onChange={() => updateAnswer(question.id, '')} />
                    不作答
                  </label>}
                  {problem && <p className="form-error" id={`${field}-error`} role="alert">{problem}</p>}
                </fieldset>
              ) : (
                <div className="field" key={question.id}>
                  <label htmlFor={field}>{label}</label>
                  <textarea id={field} rows={3} dir="auto" value={value} readOnly={sending} required={question.required}
                    aria-invalid={!!problem || undefined} aria-describedby={`${field}-help`}
                    onChange={(event) => updateAnswer(question.id, event.currentTarget.value)} />
                  <p className={problem ? 'form-error' : 'questionnaire-note'} id={`${field}-help`} role={problem ? 'alert' : undefined}>
                    {problem ?? '最多输入 320 个 UTF-8 字节。'}
                  </p>
                </div>
              )
            })}
            {path.endedEarly && <p className="questionnaire-note questionnaire-branch-notice" role="status">
              当前选项已触发结束问卷，已跳过后续题目，可直接提交答卷。
            </p>}
            {sendError && <p className="form-error" role="alert">{sendError}</p>}
            <div className="form-actions">
              <button className="button primary" type="submit" aria-disabled={sending}>
                {sending ? '正在提交…' : '提交答卷'}
              </button>
            </div>
          </form>
        </> : <p role="status">正在读取问卷…</p>}
    </div>
  </main>
}
