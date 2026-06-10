-- Diamond Neovim Plugin — Persistent Chat Panel

local M = {}

local config = {
  server = 'http://localhost:7331',
  vault  = vim.fn.expand('~/Documents'),
  keymaps = {
    chat       = '<leader>dd',
    explain    = '<leader>de',
    quiz       = '<leader>dq',
    progress   = '<leader>dp',
    weak_areas = '<leader>dw',
    health     = '<leader>dh',
    lcd        = '<leader>dl',
    exercise   = '<leader>dx',
    submit     = '<leader>ds',
    flashcards = '<leader>df',
    send_code  = '<leader>dc',
  },
}

local SEP = string.rep('─', 58)

-- ── State ─────────────────────────────────────────────────────────────────────
local chat   = { buf = nil, win = nil }
local quiz_st = { id = nil, topic = nil }
local ex_st   = { id = nil, topic = nil, language = nil }

-- ── Text helpers ──────────────────────────────────────────────────────────────
local function wrap(text, w)
  local out = {}
  for block in (text .. '\n'):gmatch('(.-)\n') do
    if #block == 0 then
      table.insert(out, '')
    else
      while #block > w do
        local cut = block:sub(1, w):match('.*%s') or block:sub(1, w)
        table.insert(out, cut)
        block = block:sub(#cut + 1):gsub('^%s+', '')
      end
      if #block > 0 then table.insert(out, block) end
    end
  end
  return out
end

local function pct(m) return math.floor((m or 0) * 100) end
local function bar(m)
  local f = math.floor((m or 0) * 10)
  return string.rep('█', f) .. string.rep('░', 10 - f)
end

-- ── HTTP helpers ──────────────────────────────────────────────────────────────
local function post(path, data, cb)
  local url  = config.server .. path
  local body = vim.fn.json_encode(data)
  local out  = {}
  vim.fn.jobstart(
    { 'curl', '-s', '-X', 'POST', '-H', 'Content-Type: application/json',
      '-d', body, '--max-time', '120', url },
    {
      stdout_buffered = true,
      on_stdout = function(_, lines) out = lines end,
      on_exit   = function(_, code)
        if code ~= 0 then cb(nil, 'curl error ' .. code); return end
        local raw = table.concat(out, '')
        local ok, dec = pcall(vim.fn.json_decode, raw)
        if not ok then cb(nil, 'bad JSON: ' .. raw:sub(1, 120)); return end
        cb(dec, nil)
      end,
    }
  )
end

local function get(path, cb)
  local out = {}
  vim.fn.jobstart(
    { 'curl', '-s', '--max-time', '10', config.server .. path },
    {
      stdout_buffered = true,
      on_stdout = function(_, lines) out = lines end,
      on_exit   = function(_, code)
        if code ~= 0 then cb(nil, 'curl error ' .. code); return end
        local ok, dec = pcall(vim.fn.json_decode, table.concat(out, ''))
        if not ok then cb(nil, 'bad JSON'); return end
        cb(dec, nil)
      end,
    }
  )
end

-- ── Chat buffer helpers ───────────────────────────────────────────────────────
local function buf_ok() return chat.buf ~= nil and vim.api.nvim_buf_is_valid(chat.buf) end
local function win_ok() return chat.win ~= nil and vim.api.nvim_win_is_valid(chat.win) end

local function buf_append(lines)
  if not buf_ok() then return end
  vim.api.nvim_set_option_value('modifiable', true, { buf = chat.buf })
  local n = vim.api.nvim_buf_line_count(chat.buf)
  vim.api.nvim_buf_set_lines(chat.buf, n, n, false, lines)
  if win_ok() then
    vim.api.nvim_win_set_cursor(chat.win, { vim.api.nvim_buf_line_count(chat.buf), 0 })
  end
end

-- Replace the last line ("⏳ thinking...") with response + separator + blank input line
local function finish_response(resp_lines)
  if not buf_ok() then return end
  vim.api.nvim_set_option_value('modifiable', true, { buf = chat.buf })
  local n   = vim.api.nvim_buf_line_count(chat.buf)
  local new = {}
  for _, l in ipairs(resp_lines) do table.insert(new, l) end
  table.insert(new, '')
  table.insert(new, SEP)
  table.insert(new, '')
  vim.api.nvim_buf_set_lines(chat.buf, n - 1, n, false, new)
  if win_ok() then
    local fc = vim.api.nvim_buf_line_count(chat.buf)
    vim.api.nvim_win_set_cursor(chat.win, { fc, 0 })
    if vim.api.nvim_get_current_win() == chat.win then
      vim.cmd('startinsert!')
    end
  end
end

local function chat_error(err)
  finish_response({ '**Error:** ' .. (err or 'unknown') })
end

-- ── Open / ensure chat panel ──────────────────────────────────────────────────
local function ensure_chat()
  if not buf_ok() then
    chat.buf = vim.api.nvim_create_buf(false, true)
    vim.api.nvim_buf_set_name(chat.buf, 'DiamondChat')
    vim.api.nvim_set_option_value('filetype',  'markdown', { buf = chat.buf })
    vim.api.nvim_set_option_value('buftype',   'nofile',   { buf = chat.buf })
    vim.api.nvim_set_option_value('swapfile',  false,      { buf = chat.buf })
    vim.api.nvim_set_option_value('modifiable', true,      { buf = chat.buf })
    vim.api.nvim_buf_set_lines(chat.buf, 0, -1, false, {
      '# Diamond AI',
      '',
      '  <CR>          send message (normal mode)',
      '  <leader>dc    ask about current buffer',
      '  <leader>ds    submit exercise from current buffer',
      '  <leader>dx    start a coding exercise',
      '',
      SEP,
      '',
    })
    vim.keymap.set('n', '<CR>', function() M._submit() end,
      { buffer = chat.buf, silent = true, desc = 'Diamond: send' })
  end

  if not win_ok() then
    local prev = vim.api.nvim_get_current_win()
    vim.cmd('botright vsplit')
    chat.win = vim.api.nvim_get_current_win()
    vim.api.nvim_win_set_buf(chat.win, chat.buf)
    vim.api.nvim_win_set_width(chat.win, 62)
    vim.api.nvim_set_option_value('wrap',        true, { win = chat.win })
    vim.api.nvim_set_option_value('linebreak',   true, { win = chat.win })
    vim.api.nvim_set_option_value('winfixwidth', true, { win = chat.win })
    vim.api.nvim_set_current_win(prev)
  end
end

-- ── Submit handler (called from <CR> in chat buffer) ─────────────────────────
function M._submit()
  if not buf_ok() then return end

  local count = vim.api.nvim_buf_line_count(chat.buf)
  local lines = vim.api.nvim_buf_get_lines(chat.buf, 0, count, false)

  -- find the last separator
  local sep_i = 0
  for i = #lines, 1, -1 do
    if lines[i] == SEP then sep_i = i; break end
  end

  -- collect everything after the separator, strip blank padding
  local raw = {}
  for i = sep_i + 1, #lines do table.insert(raw, lines[i]) end
  while #raw > 0 and raw[1]    == '' do table.remove(raw, 1) end
  while #raw > 0 and raw[#raw] == '' do table.remove(raw) end
  if #raw == 0 then return end

  local msg = table.concat(raw, '\n')

  -- display user message + thinking placeholder
  vim.api.nvim_set_option_value('modifiable', true, { buf = chat.buf })
  local display = { '**You:** ' .. raw[1] }
  for i = 2, #raw do table.insert(display, '         ' .. raw[i]) end
  table.insert(display, '')
  table.insert(display, '⏳ thinking...')
  -- sep_i is 1-based lua index; as 0-based nvim line it equals sep_i (lua i → nvim i-1, so after sep = nvim sep_i)
  vim.api.nvim_buf_set_lines(chat.buf, sep_i, count, false, display)

  if win_ok() then
    vim.api.nvim_win_set_cursor(chat.win, { vim.api.nvim_buf_line_count(chat.buf), 0 })
  end

  if quiz_st.id then
    post('/api/quiz/answer', { session_id = quiz_st.id, answer = msg }, function(data, err)
      vim.schedule(function()
        if err or not data then chat_error(err); return end
        local resp = {
          data.correct and '**✓ Correct!**' or '**✗ Incorrect**',
          string.format('Mastery: %s %d%%  |  Next: %s',
            bar(data.new_mastery), pct(data.new_mastery), data.difficulty or '?'),
          '',
        }
        for _, l in ipairs(wrap(data.feedback or '', 58)) do table.insert(resp, l) end
        if data.done then
          quiz_st = { id = nil, topic = nil }
          table.insert(resp, ''); table.insert(resp, '─── Quiz complete! ───')
        elseif data.next_question and data.next_question ~= '' then
          table.insert(resp, ''); table.insert(resp, '**Next question:**')
          for _, l in ipairs(wrap(data.next_question, 58)) do table.insert(resp, l) end
          table.insert(resp, '')
          table.insert(resp, '_Type your answer and press <CR>_')
        end
        finish_response(resp)
      end)
    end)
  else
    post('/api/ask', { prompt = msg, filetype = '' }, function(data, err)
      vim.schedule(function()
        if err or not data then chat_error(err); return end
        finish_response(wrap(data.response or '(no response)', 58))
      end)
    end)
  end
end

-- ── Commands ──────────────────────────────────────────────────────────────────

function M.open()
  ensure_chat()
  if win_ok() then
    vim.api.nvim_set_current_win(chat.win)
    local lc = vim.api.nvim_buf_line_count(chat.buf)
    vim.api.nvim_win_set_cursor(chat.win, { lc, 0 })
    vim.cmd('startinsert!')
  end
end

function M.explain()
  local mode = vim.fn.mode()
  local topic, ctx

  if mode:find('[vV]') then
    vim.cmd('normal! \027')
    local s  = vim.fn.getpos("'<")
    local e  = vim.fn.getpos("'>")
    local ls = vim.api.nvim_buf_get_lines(0, s[2]-1, e[2], false)
    topic    = table.concat(ls, ' '):gsub('%s+', ' '):sub(1, 200)
  else
    topic = vim.fn.expand('<cword>')
  end
  if topic == '' then
    vim.notify('Diamond: cursor on a word or select text', vim.log.levels.WARN); return
  end
  ctx = table.concat(vim.api.nvim_buf_get_lines(0, 0, 50, false), '\n')

  ensure_chat()
  buf_append({ '', '**Explaining:** ' .. topic, '', '⏳ thinking...' })

  post('/api/explain', { topic = topic, context = ctx }, function(data, err)
    vim.schedule(function()
      if err or not data then chat_error(err); return end
      local resp = {
        '# ' .. (data.topic or topic),
        string.format('Level: **%s**  |  Mastery: %s %d%%',
          data.level or '?', bar(data.mastery), pct(data.mastery)),
        '',
      }
      for _, l in ipairs(wrap(data.explanation or '', 58)) do table.insert(resp, l) end
      finish_response(resp)
    end)
  end)
end

function M.quiz(topic_arg)
  local topic = topic_arg or vim.fn.input('Quiz topic: ')
  if topic == '' then return end

  ensure_chat()
  buf_append({ '', '**Starting quiz:** ' .. topic, '', '⏳ thinking...' })

  post('/api/quiz/start', { topic = topic }, function(data, err)
    vim.schedule(function()
      if err or not data then chat_error(err); return end
      quiz_st = { id = data.session_id, topic = topic }
      local resp = {
        '# Quiz: ' .. topic,
        string.format('Difficulty: **%s**  |  Mastery: %s %d%%',
          data.difficulty or '?', bar(data.mastery), pct(data.mastery)),
        '',
      }
      for _, l in ipairs(wrap(data.question or '', 58)) do table.insert(resp, l) end
      table.insert(resp, '')
      table.insert(resp, '_Type your answer below and press <CR>_')
      finish_response(resp)
    end)
  end)
end

function M.progress()
  ensure_chat()
  buf_append({ '', '**Fetching progress...**', '', '⏳ thinking...' })
  get('/api/progress', function(data, err)
    vim.schedule(function()
      if err or not data then chat_error(err); return end
      local topics = data.topics or {}
      local resp   = { '# Progress  (' .. #topics .. ' topics)', '' }
      if #topics == 0 then
        table.insert(resp, 'No topics yet — start learning!')
      else
        table.insert(resp, string.format('%-20s  %-12s  %s', 'Topic', 'Mastery', 'Level'))
        table.insert(resp, string.rep('─', 44))
        for _, t in ipairs(topics) do
          table.insert(resp, string.format('%-20s  %s %3d%%  %s',
            t.topic:sub(1,20), bar(t.mastery), pct(t.mastery), t.difficulty or '?'))
        end
      end
      finish_response(resp)
    end)
  end)
end

function M.weak_areas()
  ensure_chat()
  buf_append({ '', '**Fetching weak areas...**', '', '⏳ thinking...' })
  get('/api/weak-areas', function(data, err)
    vim.schedule(function()
      if err or not data then chat_error(err); return end
      local areas = data.weak_areas or {}
      local resp  = { '# Weak Areas', '' }
      if #areas == 0 then
        table.insert(resp, 'Nothing weak — keep practicing!')
      else
        for i, t in ipairs(areas) do
          table.insert(resp, string.format('%d. %-20s  %s %d%%  (%s)',
            i, t.topic, bar(t.mastery), pct(t.mastery), t.difficulty or '?'))
        end
        table.insert(resp, ''); table.insert(resp, '_Quiz on these with <leader>dq_')
      end
      finish_response(resp)
    end)
  end)
end

function M.ask(prompt_arg)
  local mode = vim.fn.mode()
  local ctx  = ''
  if mode:find('[vV]') then
    vim.cmd('normal! \027')
    local s  = vim.fn.getpos("'<")
    local e  = vim.fn.getpos("'>")
    local ls = vim.api.nvim_buf_get_lines(0, s[2]-1, e[2], false)
    ctx = table.concat(ls, '\n')
  end

  local prompt = prompt_arg or vim.fn.input('Ask Diamond: ')
  if prompt == '' then return end

  ensure_chat()
  buf_append({ '', '**You:** ' .. prompt, '', '⏳ thinking...' })

  post('/api/ask', { prompt = prompt, context = ctx, filetype = vim.bo.filetype }, function(data, err)
    vim.schedule(function()
      if err or not data then chat_error(err); return end
      finish_response(wrap(data.response or '', 58))
    end)
  end)
end

function M.review()
  local mode = vim.fn.mode()
  local ft   = vim.bo.filetype
  local code
  if mode:find('[vV]') then
    vim.cmd('normal! \027')
    local s  = vim.fn.getpos("'<")
    local e  = vim.fn.getpos("'>")
    code = table.concat(vim.api.nvim_buf_get_lines(0, s[2]-1, e[2], false), '\n')
  else
    code = table.concat(vim.api.nvim_buf_get_lines(0, 0, -1, false), '\n')
  end
  if code == '' then vim.notify('Diamond: nothing to review', vim.log.levels.WARN); return end

  ensure_chat()
  buf_append({ '', '**Code Review** (' .. (ft ~= '' and ft or 'code') .. ')', '', '⏳ thinking...' })

  post('/api/ask', {
    prompt   = 'Review this code. List bugs, issues, and improvements. Be direct.',
    context  = code,
    filetype = ft,
  }, function(data, err)
    vim.schedule(function()
      if err or not data then chat_error(err); return end
      local resp = { '# Code Review — ' .. (ft ~= '' and ft or 'code'), '' }
      for _, l in ipairs(wrap(data.response or '', 58)) do table.insert(resp, l) end
      finish_response(resp)
    end)
  end)
end

-- Send current buffer as context with a question
function M.send_code()
  local ft   = vim.bo.filetype
  local name = vim.fn.expand('%:t')
  local code = table.concat(vim.api.nvim_buf_get_lines(0, 0, -1, false), '\n')
  if code:match('^%s*$') then vim.notify('Diamond: buffer is empty', vim.log.levels.WARN); return end

  local label  = name ~= '' and name or (ft ~= '' and ft or 'buffer')
  local prompt = vim.fn.input('Question about ' .. label .. ': ')
  if prompt == '' then return end

  ensure_chat()
  buf_append({ '', '**You [' .. label .. ']:** ' .. prompt, '', '⏳ thinking...' })

  post('/api/ask', { prompt = prompt, context = code, filetype = ft }, function(data, err)
    vim.schedule(function()
      if err or not data then chat_error(err); return end
      finish_response(wrap(data.response or '', 58))
    end)
  end)
end

function M.exercise()
  local topic = vim.fn.input('Exercise topic: ')
  if topic == '' then return end
  local lang = vim.fn.input('Language [go]: ')
  if lang == '' then lang = 'go' end
  local ctx = vim.fn.input('Context (optional): ')

  ensure_chat()
  buf_append({ '', '**Starting exercise:** ' .. topic .. ' (' .. lang .. ')', '', '⏳ thinking...' })

  post('/api/exercise/start', { topic = topic, language = lang, context = ctx }, function(data, err)
    vim.schedule(function()
      if err or not data then chat_error(err); return end
      ex_st = { id = data.session_id, topic = topic, language = lang }

      local resp = {
        '# Exercise: ' .. topic,
        'Language: **' .. lang .. '**',
        '',
        '**Task:**',
      }
      for _, l in ipairs(wrap(data.task or '', 58)) do table.insert(resp, l) end

      if data.requirements and #data.requirements > 0 then
        table.insert(resp, ''); table.insert(resp, '**Requirements:**')
        for i, r in ipairs(data.requirements) do
          table.insert(resp, i .. '. ' .. r)
        end
      end
      if data.hints and #data.hints > 0 then
        table.insert(resp, ''); table.insert(resp, '**Hints:**')
        for _, h in ipairs(data.hints) do table.insert(resp, '• ' .. h) end
      end
      table.insert(resp, '')
      table.insert(resp, '_Write your solution in an adjacent buffer._')
      table.insert(resp, '_Press **<leader>ds** to submit._')
      finish_response(resp)
    end)
  end)
end

function M.submit()
  if not ex_st.id then
    vim.notify('Diamond: no active exercise — start one with ' .. config.keymaps.exercise,
      vim.log.levels.WARN)
    return
  end
  -- read from whatever buffer the user is currently in (their code buffer)
  local code = table.concat(vim.api.nvim_buf_get_lines(0, 0, -1, false), '\n')
  if code:match('^%s*$') then vim.notify('Diamond: buffer is empty', vim.log.levels.WARN); return end

  ensure_chat()
  buf_append({ '', '**Submitting exercise...**', '', '⏳ evaluating...' })

  post('/api/exercise/submit', { session_id = ex_st.id, code = code }, function(data, err)
    vim.schedule(function()
      if err or not data then chat_error(err); return end
      local resp = {
        data.passed and '# ✓ Exercise Passed!' or '# ✗ Needs Work',
        string.format('Score: %.0f%%  |  Mastery: %s %d%%',
          (data.score or 0) * 100, bar(data.new_mastery), pct(data.new_mastery)),
        '',
        '**Feedback:**',
      }
      for _, l in ipairs(wrap(data.feedback or '', 58)) do table.insert(resp, l) end
      if data.issues and #data.issues > 0 then
        table.insert(resp, ''); table.insert(resp, '**Issues:**')
        for _, v in ipairs(data.issues) do table.insert(resp, '• ' .. v) end
      end
      if data.improvements and #data.improvements > 0 then
        table.insert(resp, ''); table.insert(resp, '**Improvements:**')
        for _, v in ipairs(data.improvements) do table.insert(resp, '• ' .. v) end
      end
      if data.passed then
        ex_st = { id = nil, topic = nil, language = nil }
        table.insert(resp, ''); table.insert(resp, '─── Exercise complete! ───')
      else
        table.insert(resp, ''); table.insert(resp, '_Fix and submit again with <leader>ds_')
      end
      finish_response(resp)
    end)
  end)
end

function M.flashcards()
  local topic   = vim.fn.input('Flashcard topic: ')
  if topic == '' then return end
  local deck    = vim.fn.input('Anki deck [' .. topic .. ']: ')
  if deck == '' then deck = topic end
  local count_s = vim.fn.input('Number of cards [5]: ')
  local count   = tonumber(count_s) or 5

  ensure_chat()
  buf_append({ '', '**Generating flashcards:** ' .. topic, '', '⏳ thinking...' })

  post('/api/flashcards', { topic = topic, deck = deck, count = count }, function(data, err)
    vim.schedule(function()
      if err or not data then chat_error(err); return end
      local cards = data.cards or {}
      local resp  = {
        '# Flashcards: ' .. topic,
        string.format('%d cards  |  deck: %s', #cards, deck),
        '',
      }
      for i, card in ipairs(cards) do
        table.insert(resp, string.format('**%d.** %s', i, card.front or ''))
        local preview = (card.back or ''):match('([^\n]+)') or ''
        table.insert(resp, '   ' .. preview)
        table.insert(resp, '')
      end

      local markdown = data.markdown or ''
      if markdown ~= '' then
        local slug     = topic:lower():gsub('[^%w]+', '-'):gsub('%-$', '')
        local area_dir = config.vault .. '/01_Areas/' .. deck .. '/flashcards'
        vim.fn.mkdir(area_dir, 'p')
        local path = area_dir .. '/' .. slug .. '-flashcards.md'
        local f = io.open(path, 'w')
        if f then
          f:write(markdown); f:close()
          table.insert(resp, '_Saved → ' .. path .. '_')
        else
          table.insert(resp, '_Could not write to vault_')
        end
      end
      finish_response(resp)
    end)
  end)
end

function M.lcd(line1_arg)
  local line1 = line1_arg or vim.fn.input('LCD line 1: ')
  if line1 == '' then return end
  local line2 = vim.fn.input('LCD line 2 (optional): ')
  local ttl_s = vim.fn.input('Seconds to show [30]: ')
  local ttl   = tonumber(ttl_s) or 30

  post('/api/lcd', { line1 = line1:sub(1,16), line2 = line2:sub(1,16), ttl = ttl }, function(data, err)
    vim.schedule(function()
      if err or not data then
        vim.notify('Diamond LCD: ' .. (err or 'failed'), vim.log.levels.ERROR)
      else
        vim.notify(string.format('Diamond LCD: sent for %ds', ttl), vim.log.levels.INFO)
      end
    end)
  end)
end

function M.health()
  get('/api/health', function(data, err)
    vim.schedule(function()
      if err or not data then
        vim.notify('Diamond ✗ unreachable: ' .. (err or '?'), vim.log.levels.ERROR)
      else
        local lvl = data.ollama == 'ok' and vim.log.levels.INFO or vim.log.levels.WARN
        vim.notify(
          string.format('Diamond  status:%s  ollama:%s', data.status or '?', data.ollama or '?'), lvl)
      end
    end)
  end)
end

-- ── Setup ─────────────────────────────────────────────────────────────────────
function M.setup(opts)
  opts = opts or {}
  if opts.server  then config.server = opts.server end
  if opts.vault   then config.vault  = vim.fn.expand(opts.vault) end
  if opts.keymaps then
    for k, v in pairs(opts.keymaps) do config.keymaps[k] = v end
  end

  vim.api.nvim_create_user_command('Diamond',           function() M.open() end, {})
  vim.api.nvim_create_user_command('DiamondAsk',        function(a) M.ask(a.args ~= '' and a.args or nil) end, { nargs = '?' })
  vim.api.nvim_create_user_command('DiamondReview',     function() M.review() end, { range = true })
  vim.api.nvim_create_user_command('DiamondExplain',    function() M.explain() end, { range = true })
  vim.api.nvim_create_user_command('DiamondQuiz',       function(a) M.quiz(a.args ~= '' and a.args or nil) end, { nargs = '?' })
  vim.api.nvim_create_user_command('DiamondProgress',   function() M.progress() end, {})
  vim.api.nvim_create_user_command('DiamondWeakAreas',  function() M.weak_areas() end, {})
  vim.api.nvim_create_user_command('DiamondHealth',     function() M.health() end, {})
  vim.api.nvim_create_user_command('DiamondLCD',        function(a) M.lcd(a.args ~= '' and a.args or nil) end, { nargs = '?' })
  vim.api.nvim_create_user_command('DiamondExercise',   function() M.exercise() end, {})
  vim.api.nvim_create_user_command('DiamondSubmit',     function() M.submit() end, {})
  vim.api.nvim_create_user_command('DiamondFlashcards', function() M.flashcards() end, {})

  local km = config.keymaps
  vim.keymap.set('n',          km.chat,       M.open,       { desc = 'Diamond: open chat', silent = true })
  vim.keymap.set({ 'n', 'v' }, km.explain,    M.explain,    { desc = 'Diamond: explain', silent = true })
  vim.keymap.set('n',          km.quiz,       M.quiz,       { desc = 'Diamond: quiz', silent = true })
  vim.keymap.set('n',          km.progress,   M.progress,   { desc = 'Diamond: progress', silent = true })
  vim.keymap.set('n',          km.weak_areas, M.weak_areas, { desc = 'Diamond: weak areas', silent = true })
  vim.keymap.set('n',          km.health,     M.health,     { desc = 'Diamond: health', silent = true })
  vim.keymap.set('n',          km.lcd,        M.lcd,        { desc = 'Diamond: LCD', silent = true })
  vim.keymap.set('n',          km.exercise,   M.exercise,   { desc = 'Diamond: exercise', silent = true })
  vim.keymap.set('n',          km.submit,     M.submit,     { desc = 'Diamond: submit', silent = true })
  vim.keymap.set('n',          km.flashcards, M.flashcards, { desc = 'Diamond: flashcards', silent = true })
  vim.keymap.set({ 'n', 'v' }, km.send_code,  M.send_code,  { desc = 'Diamond: send code', silent = true })

  -- gen.nvim drop-in replacements
  vim.keymap.set({ 'n', 'v' }, '<leader>ai', M.ask,     { desc = 'Diamond: ask', silent = true })
  vim.keymap.set({ 'n', 'v' }, '<leader>ac', M.ask,     { desc = 'Diamond: ask', silent = true })
  vim.keymap.set({ 'n', 'v' }, '<leader>ar', M.review,  { desc = 'Diamond: review', silent = true })
  vim.keymap.set({ 'n', 'v' }, '<leader>ae', M.explain, { desc = 'Diamond: explain', silent = true })
end

return M
