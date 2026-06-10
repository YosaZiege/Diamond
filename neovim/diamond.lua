-- Diamond Neovim Plugin
-- Setup: require('diamond').setup({ server = 'http://192.168.x.x:7331' })
--
-- Default keymaps:
--   <leader>de  — explain word/selection
--   <leader>dq  — start quiz
--   <leader>dp  — progress overview
--   <leader>dw  — weak areas
--   <leader>dh  — health check

local M = {}

local config = {
  server = 'http://localhost:7331',
  keymaps = {
    explain    = '<leader>de',
    quiz       = '<leader>dq',
    progress   = '<leader>dp',
    weak_areas = '<leader>dw',
    health     = '<leader>dh',
    lcd        = '<leader>dl',
  },
}

-- ---- Float window ----------------------------------------------------------------

local float = { buf = nil, win = nil }

local function open_float(title, lines)
  if float.win and vim.api.nvim_win_is_valid(float.win) then
    vim.api.nvim_win_close(float.win, true)
  end

  local width  = math.min(82, vim.o.columns - 4)
  local height = math.min(math.max(#lines + 2, 5), vim.o.lines - 6)
  local row    = math.floor((vim.o.lines - height) / 2)
  local col    = math.floor((vim.o.columns - width) / 2)

  local buf = vim.api.nvim_create_buf(false, true)
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
  vim.api.nvim_set_option_value('modifiable', false, { buf = buf })
  vim.api.nvim_set_option_value('filetype', 'markdown', { buf = buf })

  local win = vim.api.nvim_open_win(buf, true, {
    relative  = 'editor',
    width     = width,
    height    = height,
    row       = row,
    col       = col,
    style     = 'minimal',
    border    = 'rounded',
    title     = ' ' .. title .. ' ',
    title_pos = 'center',
  })
  vim.api.nvim_set_option_value('wrap', true, { win = win })

  float = { buf = buf, win = win }

  for _, key in ipairs({ 'q', '<Esc>' }) do
    vim.keymap.set('n', key, function()
      if vim.api.nvim_win_is_valid(win) then vim.api.nvim_win_close(win, true) end
    end, { buffer = buf, silent = true, nowait = true })
  end
end

local function loading(title)
  open_float(title, { '', '  ⏳ Thinking...', '' })
end

-- ---- HTTP helpers ----------------------------------------------------------------

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
      on_exit = function(_, code)
        if code ~= 0 then cb(nil, 'curl error (code ' .. code .. ')'); return end
        local ok, dec = pcall(vim.fn.json_decode, table.concat(out, ''))
        if not ok then cb(nil, 'bad JSON'); return end
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
      on_exit = function(_, code)
        if code ~= 0 then cb(nil, 'curl error'); return end
        local ok, dec = pcall(vim.fn.json_decode, table.concat(out, ''))
        if not ok then cb(nil, 'bad JSON'); return end
        cb(dec, nil)
      end,
    }
  )
end

-- ---- Text helpers ----------------------------------------------------------------

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

local function pct(mastery) return math.floor((mastery or 0) * 100) end

local function mastery_bar(m)
  local filled = math.floor((m or 0) * 10)
  return string.rep('█', filled) .. string.rep('░', 10 - filled)
end

-- ---- Quiz session state ----------------------------------------------------------

local quiz = { id = nil, topic = nil }

-- ---- Commands -------------------------------------------------------------------

function M.explain()
  local mode = vim.fn.mode()
  local topic, ctx

  if mode:find('[vV]') then
    vim.cmd('normal! \027') -- exit visual
    local s = vim.fn.getpos("'<")
    local e = vim.fn.getpos("'>")
    local ls = vim.api.nvim_buf_get_lines(0, s[2]-1, e[2], false)
    topic = table.concat(ls, ' '):gsub('%s+', ' '):sub(1, 200)
  else
    topic = vim.fn.expand('<cword>')
  end

  if topic == '' then
    vim.notify('Diamond: cursor on a word or select text', vim.log.levels.WARN)
    return
  end

  -- Send up to 50 lines of context from current buffer
  local buf_lines = vim.api.nvim_buf_get_lines(0, 0, 50, false)
  ctx = table.concat(buf_lines, '\n')

  loading('Diamond: Explain')
  post('/api/explain', { topic = topic, context = ctx }, function(data, err)
    vim.schedule(function()
      if err or not data then
        open_float('Diamond: Error', { '', '  ' .. (err or 'unknown error'), '' }); return
      end
      local lines = {
        '# ' .. (data.topic or topic),
        '',
        string.format('Level: **%s**  |  Mastery: %s %d%%',
          data.level or '?', mastery_bar(data.mastery), pct(data.mastery)),
        '',
      }
      for _, l in ipairs(wrap(data.explanation or '', 78)) do
        table.insert(lines, l)
      end
      open_float('Diamond: Explain', lines)
    end)
  end)
end

function M.quiz(topic_arg)
  local topic = topic_arg or vim.fn.input('Quiz topic: ')
  if topic == '' then return end

  loading('Diamond: Quiz')
  post('/api/quiz/start', { topic = topic }, function(data, err)
    vim.schedule(function()
      if err or not data then
        open_float('Diamond: Error', { '', '  ' .. (err or 'unknown error'), '' }); return
      end
      quiz = { id = data.session_id, topic = topic }
      local lines = {
        '# Quiz: ' .. topic,
        string.format('Difficulty: **%s**  |  Mastery: %s %d%%',
          data.difficulty or '?', mastery_bar(data.mastery), pct(data.mastery)),
        '',
      }
      for _, l in ipairs(wrap(data.question or '', 78)) do table.insert(lines, l) end
      table.insert(lines, '')
      table.insert(lines, '─── Answer with  :DiamondAnswer <your answer> ───')
      open_float('Diamond: Quiz', lines)
    end)
  end)
end

function M.answer(ans)
  if not quiz.id then
    vim.notify('Diamond: no active quiz. Start with ' .. config.keymaps.quiz, vim.log.levels.WARN)
    return
  end
  if not ans or ans == '' then
    ans = vim.fn.input('Answer: ')
  end
  if ans == '' then return end

  loading('Diamond: Evaluating')
  post('/api/quiz/answer', { session_id = quiz.id, answer = ans }, function(data, err)
    vim.schedule(function()
      if err or not data then
        open_float('Diamond: Error', { '', '  ' .. (err or 'unknown error'), '' }); return
      end
      local header = data.correct and '# ✓ Correct!' or '# ✗ Incorrect'
      local lines = {
        header,
        string.format('Mastery: %s %d%%  |  Next difficulty: %s',
          mastery_bar(data.new_mastery), pct(data.new_mastery), data.difficulty or '?'),
        '',
        '**Feedback:**',
      }
      for _, l in ipairs(wrap(data.feedback or '', 78)) do table.insert(lines, l) end

      if data.done then
        quiz = { id = nil, topic = nil }
        table.insert(lines, ''); table.insert(lines, '─── Session complete! ───')
      elseif data.next_question and data.next_question ~= '' then
        table.insert(lines, '')
        table.insert(lines, '**Next question:**')
        for _, l in ipairs(wrap(data.next_question, 78)) do table.insert(lines, l) end
        table.insert(lines, '')
        table.insert(lines, '─── Answer with  :DiamondAnswer <your answer> ───')
      end
      open_float('Diamond: Quiz', lines)
    end)
  end)
end

function M.progress()
  loading('Diamond: Progress')
  get('/api/progress', function(data, err)
    vim.schedule(function()
      if err or not data then
        open_float('Diamond: Error', { '', '  ' .. (err or 'unknown error'), '' }); return
      end
      local topics = data.topics or {}
      local lines = { '# Progress  (' .. #topics .. ' topics)', '' }
      if #topics == 0 then
        table.insert(lines, 'No topics yet — start learning!')
      else
        table.insert(lines, string.format('%-22s  %-12s  %-8s  %s', 'Topic', 'Mastery', 'Tries', 'Level'))
        table.insert(lines, string.rep('─', 56))
        for _, t in ipairs(topics) do
          table.insert(lines, string.format('%-22s  %s %3d%%  %-8d  %s',
            t.topic:sub(1, 22),
            mastery_bar(t.mastery), pct(t.mastery),
            t.attempts or 0,
            t.difficulty or '?'))
        end
      end
      open_float('Diamond: Progress', lines)
    end)
  end)
end

function M.weak_areas()
  loading('Diamond: Weak Areas')
  get('/api/weak-areas', function(data, err)
    vim.schedule(function()
      if err or not data then
        open_float('Diamond: Error', { '', '  ' .. (err or 'unknown error'), '' }); return
      end
      local areas = data.weak_areas or {}
      local lines = { '# Weak Areas', '' }
      if #areas == 0 then
        table.insert(lines, 'Nothing weak — keep practicing!')
      else
        for i, t in ipairs(areas) do
          table.insert(lines, string.format('%d. %-22s  %s %d%%  (%s)',
            i, t.topic, mastery_bar(t.mastery), pct(t.mastery), t.difficulty or '?'))
        end
        table.insert(lines, '')
        table.insert(lines, 'Tip: quiz yourself on these with <leader>dq')
      end
      open_float('Diamond: Weak Areas', lines)
    end)
  end)
end

function M.lcd(line1_arg)
  local line1 = line1_arg or vim.fn.input('LCD line 1: ')
  if line1 == '' then return end
  local line2 = vim.fn.input('LCD line 2 (optional): ')
  local ttl_s = vim.fn.input('Seconds to show [30]: ')
  local ttl = tonumber(ttl_s) or 30

  post('/api/lcd', { line1 = line1:sub(1,16), line2 = line2:sub(1,16), ttl = ttl }, function(data, err)
    vim.schedule(function()
      if err or not data then
        vim.notify('Diamond LCD: ' .. (err or 'failed'), vim.log.levels.ERROR)
        return
      end
      vim.notify(string.format('Diamond LCD: sent for %ds', ttl), vim.log.levels.INFO)
    end)
  end)
end

function M.health()
  get('/api/health', function(data, err)
    vim.schedule(function()
      if err or not data then
        vim.notify('Diamond ✗ server unreachable: ' .. (err or '?'), vim.log.levels.ERROR)
        return
      end
      local lvl = data.ollama == 'ok' and vim.log.levels.INFO or vim.log.levels.WARN
      vim.notify(
        string.format('Diamond ✓  status:%s  ollama:%s', data.status or '?', data.ollama or '?'), lvl)
    end)
  end)
end

-- ---- Setup ----------------------------------------------------------------------

function M.setup(opts)
  opts = opts or {}
  if opts.server then config.server = opts.server end
  if opts.keymaps then
    for k, v in pairs(opts.keymaps) do config.keymaps[k] = v end
  end

  -- User commands
  vim.api.nvim_create_user_command('DiamondExplain',  function() M.explain() end, { range = true })
  vim.api.nvim_create_user_command('DiamondQuiz',     function(a) M.quiz(a.args ~= '' and a.args or nil) end, { nargs = '?' })
  vim.api.nvim_create_user_command('DiamondAnswer',   function(a) M.answer(a.args) end, { nargs = '+' })
  vim.api.nvim_create_user_command('DiamondProgress', function() M.progress() end, {})
  vim.api.nvim_create_user_command('DiamondWeakAreas',function() M.weak_areas() end, {})
  vim.api.nvim_create_user_command('DiamondHealth',   function() M.health() end, {})
  vim.api.nvim_create_user_command('DiamondLCD',      function(a) M.lcd(a.args ~= '' and a.args or nil) end, { nargs = '?' })

  -- Keymaps
  local km = config.keymaps
  vim.keymap.set({ 'n', 'v' }, km.explain,    M.explain,    { desc = 'Diamond: explain', silent = true })
  vim.keymap.set('n',          km.quiz,       M.quiz,       { desc = 'Diamond: quiz', silent = true })
  vim.keymap.set('n',          km.progress,   M.progress,   { desc = 'Diamond: progress', silent = true })
  vim.keymap.set('n',          km.weak_areas, M.weak_areas, { desc = 'Diamond: weak areas', silent = true })
  vim.keymap.set('n',          km.health,     M.health,     { desc = 'Diamond: health check', silent = true })
  vim.keymap.set('n',          km.lcd,        M.lcd,        { desc = 'Diamond: send LCD message', silent = true })
end

return M
