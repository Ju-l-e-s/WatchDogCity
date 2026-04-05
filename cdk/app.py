import aws_cdk as cdk
from watchdog_stack import WatchdogStack

app = cdk.App()
WatchdogStack(app, "WatchdogStack")
app.synth()
