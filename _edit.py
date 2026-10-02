import io 
p = "web/add-agent.html" 
s = io.open(p, encoding="utf-8").read() 
LT = chr(60) 
GT = chr(62) 
Q = chr(39) 
old = LT + "option value=" + Q + "deepseek" + Q + GT + "DeepSeek HTTP agent" + LT + "/option" + GT 
new = LT + "option value=" + Q + "deepseek" + Q + GT + "HTTPS specialised agent" + LT + "/option" + GT 
s2 = s.replace(old, new) 
io.open(p, "w", encoding="utf-8").write(s2) 
print("replaced:", s2 != s, "DeepSeek now:", s2.count("DeepSeek")) 
